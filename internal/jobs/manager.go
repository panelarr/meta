// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/metrics"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
)

// validID is the shared contract's job ID rule. A ':' would read as a
// custom verb in the REST paths.
var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Manager knows every job, schedules them and records the runs of the
// jobs it runs itself.
type Manager struct {
	st        *store.Store
	log       *slog.Logger
	metrics   *metrics.Metrics
	retention int

	mu      sync.Mutex
	funcs   []*funcJob
	sources []Source
	cron    *cron.Cron
	scheds  map[string]cron.Schedule
	base    context.Context
	wg      sync.WaitGroup
}

// New returns a Manager keeping the newest retention runs per job.
func New(st *store.Store, log *slog.Logger, retention int) *Manager {
	if retention <= 0 {
		retention = 100
	}
	return &Manager{
		st: st, log: log, retention: retention,
		cron:   cron.New(cron.WithParser(config.CronParser), cron.WithLocation(time.UTC)),
		scheds: map[string]cron.Schedule{},
		base:   context.Background(),
	}
}

// SetMetrics records job runs in m.
func (m *Manager) SetMetrics(mt *metrics.Metrics) { m.metrics = mt }

// Retention is how many finished runs are kept per job.
func (m *Manager) Retention() int { return m.retention }

// Func is the body of a job the manager runs. Returning an error fails the
// run; when ctx was cancelled the run is recorded as cancelled.
type Func func(ctx context.Context, r *Recorder) error

// Add registers a job the manager runs and records itself.
func (m *Manager) Add(def Def, fn Func) {
	if !validID.MatchString(def.ID) {
		panic(fmt.Sprintf("jobs: invalid job ID %q", def.ID))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.funcs = append(m.funcs, &funcJob{m: m, def: def, fn: fn})
}

// AddSource registers jobs that run and record themselves.
func (m *Manager) AddSource(s Source) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sources = append(m.sources, s)
}

// Jobs lists every job: the sources' first (providers), then the
// manager's own, each in registration order.
func (m *Manager) Jobs() []Job {
	m.mu.Lock()
	sources, funcs := m.sources, m.funcs
	m.mu.Unlock()
	var out []Job
	for _, s := range sources {
		out = append(out, s.Jobs()...)
	}
	for _, f := range funcs {
		out = append(out, f)
	}
	return out
}

// Job returns a job by ID.
func (m *Manager) Job(id string) (Job, bool) {
	for _, j := range m.Jobs() {
		if j.Def().ID == id {
			return j, true
		}
	}
	return nil, false
}

// Start marks runs a stopped meta left running as failed, schedules every
// job with a schedule and starts the scheduler. Runs started from now on
// stop when base ends.
func (m *Manager) Start(base context.Context) error {
	if n, err := m.st.FailInterruptedJobRuns(base); err != nil {
		return err
	} else if n > 0 {
		m.log.Warn("marked interrupted job runs as failed", "runs", n)
	}
	m.mu.Lock()
	m.base = base
	m.mu.Unlock()
	for _, j := range m.Jobs() {
		def := j.Def()
		sched, err := parseSchedule(def.Schedule)
		if err != nil {
			return fmt.Errorf("job %s: %w", def.ID, err)
		}
		if sched == nil {
			continue
		}
		id := def.ID
		m.cron.Schedule(sched, cron.FuncJob(func() { m.scheduled(id) }))
		m.mu.Lock()
		m.scheds[id] = sched
		m.mu.Unlock()
		m.log.Info("job scheduled", "job", id, "cron", def.Schedule.Cron, "interval", def.Schedule.Interval)
	}
	m.cron.Start()
	return nil
}

func parseSchedule(s Schedule) (cron.Schedule, error) {
	switch {
	case !s.Enabled:
		return nil, nil
	case s.Cron != "":
		return config.CronParser.Parse(s.Cron)
	case s.Interval > 0:
		return cron.Every(s.Interval), nil
	}
	return nil, nil
}

func (m *Manager) scheduled(id string) {
	j, ok := m.Job(id)
	if !ok {
		return
	}
	_, err := m.RunJob(m.context(), id, Request{Trigger: TriggerSchedule})
	var busy *RunningError
	switch {
	case errors.As(err, &busy):
		m.log.Info("scheduled run skipped: the job is still running", "job", id, "run", busy.RunID)
		if f, ok := j.(*funcJob); ok {
			f.recordSkipped(m.context(), busy)
		}
	case err != nil:
		m.log.Error("scheduled run did not start", "job", id, "err", err)
	}
}

func (m *Manager) context() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.base
}

// Stop stops scheduling. Running runs go on until their context ends.
func (m *Manager) Stop() { <-m.cron.Stop().Done() }

// Wait blocks until the manager's own runs have stopped.
func (m *Manager) Wait() { m.wg.Wait() }

// NextRun returns when a job runs next on its schedule.
func (m *Manager) NextRun(id string) (time.Time, bool) {
	m.mu.Lock()
	s, ok := m.scheds[id]
	m.mu.Unlock()
	if !ok {
		return time.Time{}, false
	}
	return s.Next(time.Now().UTC()), true
}

// RunJob checks the params and starts a run in the background.
func (m *Manager) RunJob(base context.Context, id string, req Request) (Summary, error) {
	j, ok := m.Job(id)
	if !ok {
		return Summary{}, fmt.Errorf("%w: no job %q", ErrNotFound, id)
	}
	params, err := Resolve(j.Def(), req.Params)
	if err != nil {
		return Summary{}, err
	}
	req.Params = params
	return j.Start(base, req)
}

// RunAndWait runs one of the manager's own jobs and returns when it is
// done, for the command line.
func (m *Manager) RunAndWait(ctx context.Context, id string, req Request) (Summary, error) {
	j, ok := m.Job(id)
	if !ok {
		return Summary{}, fmt.Errorf("%w: no job %q", ErrNotFound, id)
	}
	f, ok := j.(*funcJob)
	if !ok {
		return Summary{}, fmt.Errorf("job %s can't run in the foreground", id)
	}
	params, err := Resolve(f.def, req.Params)
	if err != nil {
		return Summary{}, err
	}
	req.Params = params
	run, ctx, err := f.begin(ctx, req)
	if err != nil {
		return Summary{}, err
	}
	err = f.execute(ctx, run)
	final, gerr := m.st.GetJobRun(context.WithoutCancel(ctx), run.ID)
	if gerr != nil {
		return summary(run.JobRun), errors.Join(err, gerr)
	}
	return summary(final), err
}

// GetRun returns a run of any job.
func (m *Manager) GetRun(ctx context.Context, id string) (Run, error) {
	rid, err := uuid.Parse(id)
	if err != nil {
		return Run{}, fmt.Errorf("%w: no run %q", ErrNotFound, id)
	}
	r, err := m.st.GetJobRun(ctx, rid)
	switch {
	case err == nil:
		return details(r), nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Run{}, err
	}
	m.mu.Lock()
	sources := m.sources
	m.mu.Unlock()
	for _, s := range sources {
		run, ok, err := s.FindRun(ctx, rid)
		if err != nil {
			return Run{}, err
		}
		if ok {
			return run, nil
		}
	}
	return Run{}, fmt.Errorf("%w: no run %s", ErrNotFound, id)
}

// Cancel stops a run of any job.
func (m *Manager) Cancel(ctx context.Context, runID string) (Summary, error) {
	run, err := m.GetRun(ctx, runID)
	if err != nil {
		return Summary{}, err
	}
	j, ok := m.Job(run.JobID)
	if !ok {
		return Summary{}, fmt.Errorf("%w: no job %q", ErrNotFound, run.JobID)
	}
	if !j.Def().Cancellable {
		return Summary{}, ErrNotCancellable
	}
	if run.Status != StatusRunning {
		return Summary{}, fmt.Errorf("%w: it already ended (%s)", ErrNotRunning, run.Status)
	}
	if err := j.Cancel(ctx, runID); err != nil {
		return Summary{}, err
	}
	return run.Summary, nil
}

// lastFuncRuns returns the latest run of each of the manager's own jobs.
func (m *Manager) lastFuncRuns(ctx context.Context) (map[string]Summary, error) {
	rows, err := m.st.LatestJobRuns(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Summary{}
	for _, r := range rows {
		out[r.JobID] = summary(r)
	}
	return out, nil
}

// Last returns a job's latest run, if any.
func (m *Manager) Last(ctx context.Context, j Job) (*Summary, error) {
	runs, err := j.Runs(ctx, "", 1, 0)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	return &runs[0], nil
}

// LastRuns returns the latest run of every job, by job ID, in few queries.
func (m *Manager) LastRuns(ctx context.Context, all []Job) (map[string]Summary, error) {
	out, err := m.lastFuncRuns(ctx)
	if err != nil {
		return nil, err
	}
	for _, j := range all {
		if _, own := j.(*funcJob); own {
			continue
		}
		last, err := m.Last(ctx, j)
		if err != nil {
			return nil, err
		}
		if last != nil {
			out[j.Def().ID] = *last
		}
	}
	return out, nil
}

func summary(r db.JobRun) Summary {
	s := Summary{
		ID: r.ID.String(), JobID: r.JobID, Status: r.Status, Trigger: r.Trigger,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Message: r.Message, TriggeredBy: r.TriggeredBy,
	}
	_ = json.Unmarshal(r.Counts, &s.Counts)
	return s
}

func details(r db.JobRun) Run {
	run := Run{Summary: summary(r)}
	_ = json.Unmarshal(r.Params, &run.Params)
	_ = json.Unmarshal(r.Notes, &run.Notes)
	if len(r.Progress) > 0 {
		var p Progress
		if json.Unmarshal(r.Progress, &p) == nil {
			run.Progress = &p
		}
	}
	return run
}
