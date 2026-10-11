// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
)

// funcJob is a job the manager runs: one run at a time across every meta
// process on the database (a session advisory lock), recorded in job_runs.
type funcJob struct {
	m   *Manager
	def Def
	fn  Func

	mu      sync.Mutex
	current uuid.UUID
	cancel  context.CancelFunc
}

func (f *funcJob) Def() Def { return f.def }

// active is a run that began: its row, its lock and how to cancel it.
type active struct {
	db.JobRun
	conn   *pgxpool.Conn
	cancel context.CancelFunc
	params map[string]string
}

func (f *funcJob) Start(base context.Context, req Request) (Summary, error) {
	run, ctx, err := f.begin(base, req)
	if err != nil {
		return Summary{}, err
	}
	f.m.wg.Add(1)
	go func() {
		defer f.m.wg.Done()
		_ = f.execute(ctx, run)
	}()
	return summary(run.JobRun), nil
}

func (f *funcJob) begin(base context.Context, req Request) (*active, context.Context, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current != uuid.Nil {
		return nil, nil, &RunningError{JobID: f.def.ID, RunID: f.current.String()}
	}
	st := f.m.st
	conn, err := st.Pool.Acquire(base)
	if err != nil {
		return nil, nil, err
	}
	var locked bool
	if err := conn.QueryRow(base, `SELECT pg_try_advisory_lock(hashtext('meta-job:' || $1))`, f.def.ID).Scan(&locked); err != nil {
		conn.Release()
		return nil, nil, err
	}
	if !locked {
		conn.Release()
		busy := &RunningError{JobID: f.def.ID, Elsewhere: true}
		if r, err := st.RunningJobRun(base, f.def.ID); err == nil {
			busy.RunID = r.ID.String()
		}
		return nil, nil, busy
	}
	params, _ := json.Marshal(nonNil(req.Params))
	row, err := st.CreateJobRun(base, db.CreateJobRunParams{
		ID: uuid.Must(uuid.NewV7()), JobID: f.def.ID, Trigger: req.Trigger, Status: StatusRunning,
		TriggeredBy: req.By, Params: params,
	})
	if err != nil {
		unlock(conn, f.def.ID)
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(base)
	f.current, f.cancel = row.ID, cancel
	return &active{JobRun: row, conn: conn, cancel: cancel, params: nonNil(req.Params)}, ctx, nil
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func unlock(conn *pgxpool.Conn, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('meta-job:' || $1))`, id); err != nil {
		// Closing the connection drops the lock.
		_ = conn.Conn().Close(ctx)
	}
	conn.Release()
}

func (f *funcJob) execute(ctx context.Context, run *active) (err error) {
	m := f.m
	log := m.log.With("job", f.def.ID, "run", run.ID)
	rec := &Recorder{st: m.st, run: run.ID, params: run.params, counts: map[string]int64{}}
	start := time.Now()
	m.metrics.JobStarted(f.def.ID)
	log.Info("job started", "trigger", run.Trigger)
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
		status, msg := StatusSucceeded, rec.outcome()
		switch {
		case err != nil && (errors.Is(err, context.Canceled) || ctx.Err() != nil):
			status, msg = StatusCancelled, "cancelled"
		case err != nil:
			status, msg = StatusFailed, store.Explain(err).Error()
		case rec.skipped:
			status = StatusSkipped
		}
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		counts, notes := rec.snapshot()
		// Metrics first: whoever sees the run finished sees it counted.
		m.metrics.JobFinished(f.def.ID, status, run.Trigger, time.Since(start))
		if ferr := m.st.FinishJobRun(bg, db.FinishJobRunParams{ID: run.ID, Status: status, Message: msg, Counts: counts, Notes: notes}); ferr != nil {
			log.Error("record job result", "err", ferr)
		}
		if _, perr := m.st.PruneJobRuns(bg, db.PruneJobRunsParams{JobID: f.def.ID, Keep: int32(m.retention)}); perr != nil { //nolint:gosec // retention is small
			log.Warn("prune job runs", "err", perr)
		}
		log.Info("job finished", "status", status, "message", msg, "took", time.Since(start).Round(time.Millisecond))
		run.cancel()
		unlock(run.conn, f.def.ID)
		f.mu.Lock()
		f.current, f.cancel = uuid.Nil, nil
		f.mu.Unlock()
	}()
	return f.fn(ctx, rec)
}

// recordSkipped records a scheduled run that didn't start because the job
// was still running.
func (f *funcJob) recordSkipped(ctx context.Context, busy *RunningError) {
	now := time.Now().UTC()
	if _, err := f.m.st.CreateJobRun(ctx, db.CreateJobRunParams{
		ID: uuid.Must(uuid.NewV7()), JobID: f.def.ID, Trigger: TriggerSchedule, Status: StatusSkipped,
		Params: []byte("{}"), Message: busy.Error(), FinishedAt: &now,
	}); err != nil {
		f.m.log.Warn("record skipped run", "job", f.def.ID, "err", err)
		return
	}
	f.m.metrics.JobFinished(f.def.ID, StatusSkipped, TriggerSchedule, 0)
}

func (f *funcJob) Current(ctx context.Context) (string, bool, error) {
	f.mu.Lock()
	cur := f.current
	f.mu.Unlock()
	if cur != uuid.Nil {
		return cur.String(), true, nil
	}
	r, err := f.m.st.RunningJobRun(ctx, f.def.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return r.ID.String(), true, nil
}

func (f *funcJob) Cancel(_ context.Context, runID string) error {
	if !f.def.Cancellable {
		return ErrNotCancellable
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current == uuid.Nil || f.current.String() != runID {
		return ErrNotRunning
	}
	f.cancel()
	return nil
}

func (f *funcJob) Runs(ctx context.Context, status string, limit, offset int) ([]Summary, error) {
	p := db.ListJobRunsParams{JobID: f.def.ID, Lim: int32(limit), Off: int32(offset)} //nolint:gosec // page sizes are small
	if status != "" {
		p.Status = pgtype.Text{String: status, Valid: true}
	}
	rows, err := f.m.st.ListJobRuns(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(rows))
	for _, r := range rows {
		out = append(out, summary(r))
	}
	return out, nil
}

// Recorder is what a running job reports through: counts, notes, progress
// and its outcome. Safe for concurrent use.
type Recorder struct {
	st     *store.Store
	run    uuid.UUID
	params map[string]string

	mu       sync.Mutex
	counts   map[string]int64
	notes    []Note
	progress *Progress
	message  string
	skipped  bool
	saved    time.Time
}

// RunID is the run's ID.
func (r *Recorder) RunID() uuid.UUID { return r.run }

// Params are the values the run was started with, defaults filled in.
func (r *Recorder) Params() map[string]string { return r.params }

// Add adds n to a count.
func (r *Recorder) Add(name string, n int64) {
	r.mu.Lock()
	r.counts[name] += n
	r.mu.Unlock()
}

// Set sets a count.
func (r *Recorder) Set(name string, n int64) {
	r.mu.Lock()
	r.counts[name] = n
	r.mu.Unlock()
}

// Note records a warning, error or notable event ("info", "warning",
// "error"). The oldest go once there are MaxNotes.
func (r *Recorder) Note(level, message, ref string) {
	r.mu.Lock()
	if len(r.notes) >= MaxNotes {
		r.notes = r.notes[1:]
	}
	r.notes = append(r.notes, Note{At: time.Now().UTC(), Level: level, Message: message, Ref: ref})
	r.mu.Unlock()
}

// Progress reports where the run is. It is stored at most every two
// seconds.
func (r *Recorder) Progress(ctx context.Context, phase string, done, total int64, unit string) {
	r.mu.Lock()
	r.progress = &Progress{Phase: phase, Done: done, Total: total, Unit: unit}
	due := time.Since(r.saved) >= 2*time.Second
	if due {
		r.saved = time.Now()
	}
	r.mu.Unlock()
	if due {
		r.save(ctx)
	}
}

func (r *Recorder) save(ctx context.Context) {
	counts, notes := r.snapshot()
	r.mu.Lock()
	progress, _ := json.Marshal(r.progress)
	r.mu.Unlock()
	_ = r.st.SaveJobRun(context.WithoutCancel(ctx), db.SaveJobRunParams{ID: r.run, Counts: counts, Notes: notes, Progress: progress})
}

// Message sets the run's one-line outcome.
func (r *Recorder) Message(format string, args ...any) {
	r.mu.Lock()
	r.message = fmt.Sprintf(format, args...)
	r.mu.Unlock()
}

// Skip marks the run skipped (nothing to do), with why.
func (r *Recorder) Skip(why string) {
	r.mu.Lock()
	r.skipped, r.message = true, why
	r.mu.Unlock()
}

func (r *Recorder) outcome() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.message
}

func (r *Recorder) snapshot() (counts, notes []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts, _ = json.Marshal(r.counts)
	if r.notes == nil {
		notes = []byte("[]")
	} else {
		notes, _ = json.Marshal(r.notes)
	}
	return counts, notes
}
