// SPDX-License-Identifier: GPL-3.0-or-later

// Package ingest runs syncs: it reads a plugin's Sync stream, writes the
// records through the catalog in batches committed at checkpoints, and
// records every run. It also schedules syncs.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/enums"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/sdk/plugin"
)

// Run triggers.
const (
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
	TriggerStartup  = "startup"
)

// maxNotices caps the notices kept per run.
const maxNotices = 200

// Runner runs syncs, at most one per provider at a time.
type Runner struct {
	st        *store.Store
	host      *host.Host
	writer    *catalog.Writer
	log       *slog.Logger
	batchSize int

	mu      sync.Mutex
	running map[string]uuid.UUID
	wg      sync.WaitGroup
}

// NewRunner returns a Runner.
func NewRunner(st *store.Store, h *host.Host, w *catalog.Writer, log *slog.Logger, batchSize int) *Runner {
	if batchSize <= 0 {
		batchSize = 500
	}
	return &Runner{st: st, host: h, writer: w, log: log, batchSize: batchSize, running: map[string]uuid.UUID{}}
}

// ErrRunning is returned when the provider already has a sync running.
var ErrRunning = errors.New("a sync is already running for this provider")

// Start begins a sync in the background and returns its run record. The
// sync keeps going after ctx of the caller ends; it stops when base ends.
func (r *Runner) Start(base context.Context, provider, kind, trigger string, force bool) (db.SyncRun, error) {
	run, err := r.begin(base, provider, kind, trigger)
	if err != nil {
		return db.SyncRun{}, err
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		_ = r.execute(base, run, force)
	}()
	return run, nil
}

// Run performs a sync and returns when it is done.
func (r *Runner) Run(ctx context.Context, provider, kind, trigger string, force bool) (db.SyncRun, error) {
	run, err := r.begin(ctx, provider, kind, trigger)
	if err != nil {
		return db.SyncRun{}, err
	}
	err = r.execute(ctx, run, force)
	final, gerr := r.st.GetSyncRun(context.WithoutCancel(ctx), run.ID)
	if gerr != nil {
		return run, errors.Join(err, gerr)
	}
	return final, err
}

// Wait blocks until background syncs have stopped.
func (r *Runner) Wait() { r.wg.Wait() }

// Running reports the run in progress for a provider, if any.
func (r *Runner) Running(provider string) (uuid.UUID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.running[provider]
	return id, ok
}

func (r *Runner) begin(ctx context.Context, provider, kind, trigger string) (db.SyncRun, error) {
	p, ok := r.host.Get(provider)
	if !ok {
		return db.SyncRun{}, plugin.ErrNotFound("no provider %q is configured", provider)
	}
	if err := p.Connect(ctx); err != nil {
		return db.SyncRun{}, err
	}
	if kind == "" {
		for _, k := range []string{plugin.KindDump, plugin.KindDelta} {
			if p.Supports(k) {
				kind = k
				break
			}
		}
		if kind == "" {
			kind = plugin.KindDump
		}
	}
	r.mu.Lock()
	if _, busy := r.running[provider]; busy {
		r.mu.Unlock()
		return db.SyncRun{}, ErrRunning
	}
	id := uuid.Must(uuid.NewV7())
	r.running[provider] = id
	r.mu.Unlock()

	cursor := ""
	cur, err := r.st.GetSyncCursor(ctx, db.GetSyncCursorParams{Provider: provider, Kind: kind})
	switch {
	case err == nil:
		cursor = cur.Cursor
	case !errors.Is(err, pgx.ErrNoRows):
		r.done(provider)
		return db.SyncRun{}, err
	}
	run, err := r.st.CreateSyncRun(ctx, db.CreateSyncRunParams{ID: id, Provider: provider, Kind: kind, Trigger: trigger, CursorStart: cursor})
	if err != nil {
		r.done(provider)
		return db.SyncRun{}, err
	}
	return run, nil
}

func (r *Runner) done(provider string) {
	r.mu.Lock()
	delete(r.running, provider)
	r.mu.Unlock()
}

type notice struct {
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Ref     string    `json:"ref,omitempty"`
	At      time.Time `json:"at"`
}

// state is one run in progress.
type state struct {
	r        *Runner
	run      db.SyncRun
	log      *slog.Logger
	batch    catalog.Batch
	cursor   string
	started  *metadatav1.SyncStarted
	finished *metadatav1.SyncFinished
	counts   struct{ upserts, changed, removals, merges, skipped int64 }
	notices  []notice
	progress *metadatav1.SyncProgress
	lastSave time.Time
}

func (r *Runner) execute(ctx context.Context, run db.SyncRun, force bool) (err error) {
	defer r.done(run.Provider)
	s := &state{
		r:      r,
		run:    run,
		log:    r.log.With("provider", run.Provider, "kind", run.Kind, "run", run.ID),
		cursor: run.CursorStart,
		batch:  catalog.Batch{Provider: run.Provider, Run: uuid.NullUUID{UUID: run.ID, Valid: true}},
	}
	s.log.Info("sync started", "trigger", run.Trigger, "cursor", run.CursorStart)
	start := time.Now()
	marked := int64(0)
	defer func() {
		status, msg := "succeeded", ""
		if err != nil {
			status, msg = "failed", err.Error()
			if errors.Is(err, context.Canceled) {
				status = "cancelled"
			}
		}
		// Record the outcome even if ctx is done.
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		s.saveCounts(bg, true)
		if ferr := r.st.FinishSyncRun(bg, db.FinishSyncRunParams{ID: run.ID, Status: status, Error: msg, MarkedRemoved: marked}); ferr != nil {
			s.log.Error("record sync result", "err", ferr)
		}
		s.log.Info("sync finished", "status", status, "error", msg, "upserts", s.counts.upserts,
			"changed", s.counts.changed, "merges", s.counts.merges, "removed", marked, "took", time.Since(start).Round(time.Millisecond))
	}()

	p, _ := r.host.Get(run.Provider)
	stream, release, err := p.Sync(ctx, &metadatav1.SyncRequest{Kind: run.Kind, Cursor: run.CursorStart, Force: force})
	if err != nil {
		return err
	}
	defer release()
	defer func() { _ = stream.Close() }()

	for {
		ev, rerr := stream.Recv()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			// Keep what was committed; write the rest of the batch only if
			// it is complete records (it is: each upsert is whole).
			if ferr := s.flush(ctx); ferr != nil {
				return errors.Join(rerr, ferr)
			}
			return fmt.Errorf("plugin: %w", rerr)
		}
		if err := s.handle(ctx, ev); err != nil {
			return err
		}
	}
	if err := s.flush(ctx); err != nil {
		return err
	}
	if s.finished == nil {
		return errors.New("plugin ended the sync without SyncFinished")
	}
	if s.started != nil && s.started.GetFullSnapshot() && !s.started.GetUnchanged() {
		n, err := r.writer.FinishSnapshot(ctx, run.Provider, run.ID)
		if err != nil {
			return err
		}
		marked = int64(n)
	} else if _, err := r.writer.Reconcile(ctx, run.Provider); err != nil {
		return err
	}
	if err := r.writer.RecountTags(ctx); err != nil {
		return err
	}
	s.cursor = s.finished.GetCursor()
	return s.saveCursor(ctx)
}

func (s *state) handle(ctx context.Context, ev *metadatav1.SyncResponse) error {
	switch e := ev.GetEvent().(type) {
	case *metadatav1.SyncResponse_Started:
		s.started = e.Started
		if err := s.r.st.StartSyncRun(ctx, db.StartSyncRunParams{
			ID: s.run.ID, SnapshotID: e.Started.GetSnapshotId(), FullSnapshot: e.Started.GetFullSnapshot(),
		}); err != nil {
			return err
		}
		if e.Started.GetUnchanged() {
			s.note("info", "the provider reports no changes since the last run", "")
		}
	case *metadatav1.SyncResponse_Upsert:
		s.counts.upserts++
		s.batch.Upserts = append(s.batch.Upserts, e.Upsert)
		if len(s.batch.Upserts) >= s.r.batchSize {
			return s.flush(ctx)
		}
	case *metadatav1.SyncResponse_Removed:
		s.counts.removals++
		s.batch.Removals = append(s.batch.Removals, e.Removed.GetRef().GetId())
	case *metadatav1.SyncResponse_Merge:
		s.counts.merges++
		s.batch.Merges = append(s.batch.Merges, catalog.Merge{Merged: e.Merge.GetMerged().GetId(), Into: e.Merge.GetInto().GetId()})
	case *metadatav1.SyncResponse_Checkpoint:
		if err := s.flush(ctx); err != nil {
			return err
		}
		s.cursor = e.Checkpoint.GetCursor()
		if err := s.saveCursor(ctx); err != nil {
			return err
		}
		s.saveCounts(ctx, false)
	case *metadatav1.SyncResponse_Finished:
		s.finished = e.Finished
		s.counts.skipped = e.Finished.GetSkipped()
	case *metadatav1.SyncResponse_Notice:
		ref := ""
		if e.Notice.GetRef() != nil {
			ref = e.Notice.GetRef().GetProvider() + ":" + e.Notice.GetRef().GetId()
		}
		s.note(enums.Name(e.Notice.GetLevel()), e.Notice.GetMessage(), ref)
	case *metadatav1.SyncResponse_Progress:
		s.progress = e.Progress
		s.saveCounts(ctx, false)
	}
	return nil
}

func (s *state) note(level, msg, ref string) {
	if len(s.notices) >= maxNotices {
		s.notices = s.notices[1:]
	}
	s.notices = append(s.notices, notice{Level: level, Message: msg, Ref: ref, At: time.Now().UTC()})
}

func (s *state) flush(ctx context.Context) error {
	b := s.batch
	if len(b.Upserts)+len(b.Merges)+len(b.Removals) == 0 {
		return nil
	}
	res, err := s.r.writer.Apply(ctx, b)
	if err != nil {
		return fmt.Errorf("write batch: %w", err)
	}
	s.counts.changed += int64(res.Changed)
	s.batch.Upserts, s.batch.Merges, s.batch.Removals = nil, nil, nil
	return nil
}

func (s *state) saveCursor(ctx context.Context) error {
	snapshot := ""
	if s.started != nil {
		snapshot = s.started.GetSnapshotId()
	}
	return s.r.st.UpsertSyncCursor(ctx, db.UpsertSyncCursorParams{
		Provider: s.run.Provider, Kind: s.run.Kind, Cursor: s.cursor, SnapshotID: snapshot,
		RunID: uuid.NullUUID{UUID: s.run.ID, Valid: true},
	})
}

// saveCounts records progress, at most every two seconds unless forced.
func (s *state) saveCounts(ctx context.Context, force bool) {
	if !force && time.Since(s.lastSave) < 2*time.Second {
		return
	}
	s.lastSave = time.Now()
	notices, _ := json.Marshal(s.notices)
	if s.notices == nil {
		notices = []byte("[]")
	}
	var progress []byte
	if s.progress != nil {
		progress, _ = protojson.Marshal(s.progress)
	}
	if err := s.r.st.UpdateSyncRunCounts(ctx, db.UpdateSyncRunCountsParams{
		ID: s.run.ID, Upserts: s.counts.upserts, Changed: s.counts.changed, Removals: s.counts.removals,
		Merges: s.counts.merges, Skipped: s.counts.skipped, CursorEnd: s.cursor, Notices: notices, Progress: progress,
	}); err != nil {
		s.log.Warn("record sync progress", "err", err)
	}
}
