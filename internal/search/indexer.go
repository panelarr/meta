// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/panelarr/meta/internal/metrics"
	"github.com/panelarr/meta/internal/store/db"
)

// IndexerOptions tune the outbox indexer.
type IndexerOptions struct {
	// Batch is how many outbox rows one round applies. Default 500.
	Batch int
	// Interval is how often an idle indexer looks for new rows. Default 1s.
	Interval time.Duration
	// RebuildThreshold: with more rows than this waiting, a rebuild is
	// cheaper than applying them one by one. Default 50,000.
	RebuildThreshold int64
	// OnFlood is called (at most once per minute) when the outbox passes
	// RebuildThreshold, to start a rebuild.
	OnFlood func()
	Log     *slog.Logger
	Metrics *metrics.Metrics
}

// Indexer applies the search outbox to an engine: it claims the oldest
// rows (other indexers skip them), indexes the entries' current state,
// and deletes the rows once the engine took them. A failed round leaves
// the rows for the next one (at least once; documents are idempotent).
type Indexer struct {
	x    *External
	opts IndexerOptions

	mu        sync.Mutex
	lastErr   string
	errAt     time.Time
	appliedAt time.Time
	applied   int64
	floodAt   time.Time
}

// NewIndexer returns the indexer of x.
func NewIndexer(x *External, opts IndexerOptions) *Indexer {
	if opts.Batch <= 0 {
		opts.Batch = 500
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.RebuildThreshold <= 0 {
		opts.RebuildThreshold = 50000
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &Indexer{x: x, opts: opts}
}

// Run applies the outbox until ctx ends, backing off on errors.
func (ix *Indexer) Run(ctx context.Context) {
	backoff := time.Duration(0)
	for {
		n, err := ix.Round(ctx)
		wait := ix.opts.Interval
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			backoff = min(max(2*backoff, time.Second), time.Minute)
			wait = backoff
			if !errors.Is(err, errNotBuilt) && !errors.Is(err, errRebuilding) {
				ix.opts.Log.Warn("search indexer", "err", err, "retry_in", wait)
			}
		case n > 0:
			backoff, wait = 0, 0 // more may be waiting
		default:
			backoff = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

var errRebuilding = errors.New("search: the index is being rebuilt")

// Round applies one batch of the outbox and returns how many rows it took.
func (ix *Indexer) Round(ctx context.Context) (int, error) {
	x := ix.x
	stats, err := x.q.SearchOutboxStats(ctx)
	if err != nil {
		return 0, err
	}
	lag := time.Duration(0)
	if stats.Pending > 0 {
		lag = time.Since(stats.Oldest)
	}
	ix.opts.Metrics.SearchOutbox(stats.Pending, lag)
	if stats.Pending == 0 {
		return 0, nil
	}
	// Another process may have rebuilt and swapped: write where it says.
	if _, err := x.Load(ctx); err != nil {
		return 0, ix.failed(err)
	}
	if !x.Ready() {
		ix.flood("the search index isn't built yet")
		return 0, errNotBuilt
	}
	if stats.Pending > ix.opts.RebuildThreshold {
		ix.flood("the search outbox passed rebuild_threshold")
		return 0, nil
	}

	var taken int
	err = pgx.BeginFunc(ctx, x.pool, func(tx pgx.Tx) error {
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT NOT pg_try_advisory_xact_lock_shared(hashtext($1))`, rebuildLock).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return errRebuilding
		}
		q := db.New(tx)
		rows, err := q.ClaimSearchOutbox(ctx, int32(ix.opts.Batch)) //nolint:gosec // small batch
		if err != nil || len(rows) == 0 {
			return err
		}
		seen := map[uuid.UUID]bool{}
		var ids []uuid.UUID
		seqs := make([]int64, len(rows))
		for i, r := range rows {
			seqs[i] = r.Seq
			if !seen[r.EntryID] {
				seen[r.EntryID] = true
				ids = append(ids, r.EntryID)
			}
		}
		if err := x.Upsert(ctx, ids...); err != nil {
			return err
		}
		taken = len(rows)
		return q.DeleteSearchOutbox(ctx, seqs)
	})
	if err != nil {
		return 0, ix.failed(err)
	}
	ix.mu.Lock()
	ix.applied += int64(taken)
	ix.appliedAt = time.Now()
	ix.mu.Unlock()
	return taken, nil
}

func (ix *Indexer) failed(err error) error {
	if errors.Is(err, errRebuilding) || errors.Is(err, errNotBuilt) {
		return err
	}
	ix.mu.Lock()
	ix.lastErr, ix.errAt = err.Error(), time.Now()
	ix.mu.Unlock()
	return err
}

func (ix *Indexer) flood(why string) {
	ix.mu.Lock()
	due := time.Since(ix.floodAt) > time.Minute
	if due {
		ix.floodAt = time.Now()
	}
	ix.mu.Unlock()
	if due && ix.opts.OnFlood != nil {
		ix.opts.Log.Info("starting a search index rebuild", "why", why)
		ix.opts.OnFlood()
	}
}

// IndexerStatus is what the indexer last did.
type IndexerStatus struct {
	Applied   int64
	AppliedAt time.Time
	LastErr   string
	ErrAt     time.Time
}

// Status reports the indexer's work and last error.
func (ix *Indexer) Status() IndexerStatus {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return IndexerStatus{Applied: ix.applied, AppliedAt: ix.appliedAt, LastErr: ix.lastErr, ErrAt: ix.errAt}
}

// Drain applies the whole outbox now, for "run now".
func (ix *Indexer) Drain(ctx context.Context, progress func(done int64)) (int64, error) {
	var done int64
	for {
		n, err := ix.Round(ctx)
		if err != nil {
			return done, err
		}
		if n == 0 {
			return done, nil
		}
		done += int64(n)
		if progress != nil {
			progress(done)
		}
	}
}
