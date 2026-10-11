// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/panelarr/meta/internal/metrics"
	"github.com/panelarr/meta/internal/store/db"
)

// Engine is an external search engine as meta uses it. Writes go to a
// physical index; searches go to the live name, which an atomic Activate
// points at a physical index (a Typesense alias, Meilisearch's
// swap-indexes).
type Engine interface {
	// Name is the backend name, e.g. "typesense".
	Name() string
	// Check reaches the engine.
	Check(ctx context.Context) error
	// CreateIndex makes a physical index with meta's document settings.
	CreateIndex(ctx context.Context, name string) error
	// DeleteIndex drops a physical index; a missing one is no error.
	DeleteIndex(ctx context.Context, name string) error
	// HasIndex reports whether a physical index exists.
	HasIndex(ctx context.Context, name string) (bool, error)
	// Upsert adds or replaces documents.
	Upsert(ctx context.Context, index string, docs []Document) error
	// Delete removes documents by entry ID; missing ones are no error.
	Delete(ctx context.Context, index string, ids []string) error
	// Count returns the number of documents.
	Count(ctx context.Context, index string) (int64, error)
	// Activate atomically makes the live name search index. It returns the
	// physical index writes go to from now on, and an index to drop (the
	// one that was live before), if any.
	Activate(ctx context.Context, live, index string) (current, drop string, err error)
	// Search runs a query against the live name.
	Search(ctx context.Context, live string, q Query) (Result, error)
}

// ExternalOptions configure an External backend.
type ExternalOptions struct {
	// Prefix of the engine's index names. Default "meta".
	Prefix string
	// Browse says who serves browsing (no search text): BrowseFacets (the
	// default) the engine only when facets are asked for, BrowseEngine
	// always the engine, BrowsePostgres always PostgreSQL.
	Browse string
	// Timeout of one engine query before falling back. Default 2s.
	Timeout time.Duration
	Log     *slog.Logger
	Metrics *metrics.Metrics
}

// Browse modes.
const (
	BrowseFacets   = "facets"
	BrowseEngine   = "engine"
	BrowsePostgres = "postgres"
)

// External serves searches from an engine and falls back to PostgreSQL
// when the engine fails or its index isn't built yet. PostgreSQL stays the
// source of truth: the index is rebuilt from it and fed by the outbox.
type External struct {
	eng  Engine
	pg   *Postgres
	pool *pgxpool.Pool
	q    *db.Queries
	opts ExternalOptions

	mu       sync.RWMutex
	current  string // physical index writes go to; empty: not built
	builtAt  time.Time
	docs     int64
	lastErr  string
	errAt    time.Time
	degraded bool
	loggedAt time.Time
}

// NewExternal returns an External backend for eng.
func NewExternal(eng Engine, pg *Postgres, pool *pgxpool.Pool, opts ExternalOptions) *External {
	if opts.Prefix == "" {
		opts.Prefix = "meta"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Second
	}
	if opts.Browse == "" {
		opts.Browse = BrowseFacets
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &External{eng: eng, pg: pg, pool: pool, q: db.New(pool), opts: opts}
}

// Engine returns the engine.
func (x *External) Engine() Engine { return x.eng }

// Live is the name searches use.
func (x *External) Live() string { return x.opts.Prefix + "_entries" }

// Capabilities of an engine backend.
func (x *External) Capabilities() Capabilities {
	return Capabilities{Name: x.eng.Name(), TypoTolerance: true, Substring: true, Facets: true}
}

// Load reads which index is live. It reports whether the index is usable:
// built with the current document schema and still in the engine.
func (x *External) Load(ctx context.Context) (bool, error) {
	row, err := x.q.GetSearchIndex(ctx, x.eng.Name())
	if errors.Is(err, pgx.ErrNoRows) {
		x.set("", time.Time{}, 0)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.SchemaVersion != SchemaVersion {
		x.set("", time.Time{}, 0)
		return false, nil
	}
	if ok, err := x.eng.HasIndex(ctx, row.IndexName); err != nil {
		return false, err
	} else if !ok {
		x.set("", time.Time{}, 0)
		return false, nil
	}
	x.set(row.IndexName, row.BuiltAt, row.Documents)
	return true, nil
}

func (x *External) set(current string, builtAt time.Time, docs int64) {
	x.mu.Lock()
	x.current, x.builtAt, x.docs = current, builtAt, docs
	x.mu.Unlock()
}

// Current is the physical index writes go to; empty before a build.
func (x *External) Current() string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.current
}

// Ready reports whether an index is built and searches use it.
func (x *External) Ready() bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.current != ""
}

// Query serves a search from the engine, or from PostgreSQL when the
// index isn't ready, the engine fails, or it is a browse without facets.
func (x *External) Query(ctx context.Context, q Query) (Result, error) {
	if !x.Ready() || (q.Text == "" && !x.browses(q)) {
		return x.pg.Query(ctx, q)
	}
	qctx, cancel := context.WithTimeout(ctx, x.opts.Timeout)
	defer cancel()
	res, err := x.eng.Search(qctx, x.Live(), q)
	if err == nil {
		x.recover()
		res.Backend = x.eng.Name()
		return res, nil
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	x.fail(err)
	return x.pg.Query(ctx, q)
}

// browses reports whether the engine serves a browse.
func (x *External) browses(q Query) bool {
	switch x.opts.Browse {
	case BrowseEngine:
		return true
	case BrowseFacets:
		return q.Facets
	}
	return false
}

// fail notes an engine error and logs it at most once a minute.
func (x *External) fail(err error) {
	x.mu.Lock()
	x.degraded, x.lastErr, x.errAt = true, err.Error(), time.Now()
	logIt := time.Since(x.loggedAt) > time.Minute
	if logIt {
		x.loggedAt = time.Now()
	}
	x.mu.Unlock()
	x.opts.Metrics.SearchFallback(x.eng.Name())
	if logIt {
		x.opts.Log.Warn("search engine failed; answering from PostgreSQL", "backend", x.eng.Name(), "err", err)
	}
}

func (x *External) recover() {
	x.mu.Lock()
	if x.degraded {
		x.opts.Log.Info("search engine answers again", "backend", x.eng.Name())
	}
	x.degraded = false
	x.mu.Unlock()
}

// Upsert indexes the current state of entries: active ones are written,
// others removed.
func (x *External) Upsert(ctx context.Context, ids ...uuid.UUID) error {
	x.mu.RLock()
	index := x.current
	x.mu.RUnlock()
	if index == "" {
		return errNotBuilt
	}
	return x.apply(ctx, index, ids)
}

var errNotBuilt = errors.New("search: the index isn't built yet")

func (x *External) apply(ctx context.Context, index string, ids []uuid.UUID) error {
	docs, gone, err := LoadDocuments(ctx, x.pool, ids)
	if err != nil {
		return err
	}
	if len(docs) > 0 {
		if err := x.eng.Upsert(ctx, index, docs); err != nil {
			return err
		}
	}
	if len(gone) > 0 {
		del := make([]string, len(gone))
		for i, id := range gone {
			del[i] = id.String()
		}
		if err := x.eng.Delete(ctx, index, del); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes entries from the index.
func (x *External) Delete(ctx context.Context, ids ...uuid.UUID) error {
	return x.Upsert(ctx, ids...) // gone entries are deleted
}

// Rebuild builds a new index from the database and swaps it in.
func (x *External) Rebuild(ctx context.Context) error {
	_, err := x.RebuildWith(ctx, nil)
	return err
}

// rebuildLock keeps the outbox indexer of every meta process off the
// index while it is rebuilt.
const rebuildLock = "meta-search-rebuild"

// RebuildWith builds a new physical index from every active entry, swaps
// it in atomically and drops the old one. Searches use the old index
// until the swap. Outbox rows written before the build started are
// covered by it and removed; later ones are applied by the indexer after
// the swap. It returns the number of documents.
func (x *External) RebuildWith(ctx context.Context, progress func(done, total int64)) (int64, error) {
	conn, err := x.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, rebuildLock).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		return 0, errors.New("search: a rebuild is already running in another meta process")
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext($1))`, rebuildLock)
	}()

	head, err := x.q.SearchOutboxHead(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	if err := x.pool.QueryRow(ctx, `SELECT count(*) FROM entries WHERE state = 'active'`).Scan(&total); err != nil {
		return 0, err
	}
	name := fmt.Sprintf("%s_entries_v%d_%d", x.opts.Prefix, SchemaVersion, time.Now().UnixMilli())
	if err := x.eng.CreateIndex(ctx, name); err != nil {
		return 0, err
	}
	built := false
	defer func() {
		if !built {
			_ = x.eng.DeleteIndex(context.WithoutCancel(ctx), name)
		}
	}()
	var after uuid.UUID
	var done int64
	for {
		ids, err := x.q.ListActiveEntryIDsAfter(ctx, db.ListActiveEntryIDsAfterParams{After: after, Lim: 1000})
		if err != nil {
			return done, err
		}
		if len(ids) == 0 {
			break
		}
		docs, _, err := LoadDocuments(ctx, x.pool, ids)
		if err != nil {
			return done, err
		}
		if err := x.eng.Upsert(ctx, name, docs); err != nil {
			return done, err
		}
		done += int64(len(docs))
		after = ids[len(ids)-1]
		if progress != nil {
			progress(done, total)
		}
	}
	current, drop, err := x.eng.Activate(ctx, x.Live(), name)
	if err != nil {
		return done, err
	}
	built = true
	if err := x.q.SetSearchIndex(ctx, db.SetSearchIndexParams{
		Backend: x.eng.Name(), IndexName: current, SchemaVersion: SchemaVersion, Documents: done,
	}); err != nil {
		return done, err
	}
	if _, err := x.q.TruncateSearchOutboxUpTo(ctx, head); err != nil {
		return done, err
	}
	x.set(current, time.Now(), done)
	if drop != "" && drop != current {
		if err := x.eng.DeleteIndex(ctx, drop); err != nil {
			x.opts.Log.Warn("drop the old search index", "index", drop, "err", err)
		}
	}
	return done, nil
}

// Status is what the admin shows about the engine.
type Status struct {
	Backend   string
	Ready     bool
	Index     string
	Documents int64
	BuiltAt   time.Time
	// Pending outbox rows and the age of the oldest.
	Pending  int64
	Lag      time.Duration
	Degraded bool
	LastErr  string
	ErrAt    time.Time
}

// Status reports the index, the outbox and the last engine error.
func (x *External) Status(ctx context.Context) (Status, error) {
	st, err := x.q.SearchOutboxStats(ctx)
	if err != nil {
		return Status{}, err
	}
	x.mu.RLock()
	s := Status{
		Backend: x.eng.Name(), Ready: x.current != "", Index: x.current, Documents: x.docs, BuiltAt: x.builtAt,
		Pending: st.Pending, Degraded: x.degraded, LastErr: x.lastErr, ErrAt: x.errAt,
	}
	x.mu.RUnlock()
	if st.Pending > 0 && !st.Oldest.IsZero() {
		s.Lag = time.Since(st.Oldest)
	}
	return s, nil
}

// Check reaches the engine; health uses it.
func (x *External) Check(ctx context.Context) error { return x.eng.Check(ctx) }
