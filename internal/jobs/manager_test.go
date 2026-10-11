// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/panelarr/meta/internal/metrics"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/storetest"
)

func newManager(t *testing.T, st *store.Store, retention int) *Manager {
	t.Helper()
	m := New(st, slog.New(slog.DiscardHandler), retention)
	m.SetMetrics(metrics.New())
	t.Cleanup(m.Wait)
	return m
}

// wait polls a run until it has finished.
func wait(t *testing.T, m *Manager, id string) Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := m.GetRun(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != StatusRunning {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never finished", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunRecordsOutcome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newManager(t, storetest.New(t), 100)
	m.Add(Def{ID: "count", Name: "Count", Params: []Param{{Name: "n", Type: ParamInt, Default: "3"}}}, func(ctx context.Context, r *Recorder) error {
		n := Int(r.Params(), "n", 0)
		for i := range n {
			r.Add("items", 1)
			r.Progress(ctx, "count", i+1, n, "items")
		}
		r.Note("warning", "item 2 looked odd", "item:2")
		r.Message("counted %d", n)
		return nil
	})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	started, err := m.RunJob(ctx, "count", Request{Trigger: TriggerManual, By: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != StatusRunning || started.TriggeredBy != "admin" {
		t.Fatalf("started: %+v", started)
	}
	run := wait(t, m, started.ID)
	if run.Status != StatusSucceeded || run.Message != "counted 3" || run.Counts["items"] != 3 ||
		run.Params["n"] != "3" || len(run.Notes) != 1 || run.Notes[0].Ref != "item:2" || run.Progress != nil || run.FinishedAt == nil {
		t.Fatalf("run: %+v", run)
	}
	j, _ := m.Job("count")
	runs, err := j.Runs(ctx, "", 10, 0)
	if err != nil || len(runs) != 1 || runs[0].ID != started.ID {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	if runs, _ := j.Runs(ctx, StatusFailed, 10, 0); len(runs) != 0 {
		t.Fatalf("failed runs: %+v", runs)
	}
	last, err := m.LastRuns(ctx, m.Jobs())
	if err != nil || last["count"].ID != started.ID {
		t.Fatalf("last runs: %+v %v", last, err)
	}
	if v := testutil.ToFloat64(m.metrics.JobRuns.WithLabelValues("count", StatusSucceeded, TriggerManual)); v != 1 {
		t.Fatalf("job runs metric: %v", v)
	}
}

func TestFailedAndPanickingRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newManager(t, storetest.New(t), 100)
	m.Add(Def{ID: "fails"}, func(context.Context, *Recorder) error { return errors.New("upstream said no") })
	m.Add(Def{ID: "panics"}, func(context.Context, *Recorder) error { panic("boom") })
	m.Add(Def{ID: "skips"}, func(_ context.Context, r *Recorder) error { r.Skip("nothing to do"); return nil })
	for id, want := range map[string][2]string{
		"fails":  {StatusFailed, "upstream said no"},
		"panics": {StatusFailed, "panic: boom"},
		"skips":  {StatusSkipped, "nothing to do"},
	} {
		s, err := m.RunJob(ctx, id, Request{Trigger: TriggerManual})
		if err != nil {
			t.Fatal(err)
		}
		if run := wait(t, m, s.ID); run.Status != want[0] || run.Message != want[1] {
			t.Errorf("%s: %s %q, want %s %q", id, run.Status, run.Message, want[0], want[1])
		}
	}
}

func TestOneRunAtATimeAndCancel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newManager(t, storetest.New(t), 100)
	release := make(chan struct{})
	m.Add(Def{ID: "slow", Cancellable: true}, func(ctx context.Context, _ *Recorder) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	})
	m.Add(Def{ID: "stubborn"}, func(context.Context, *Recorder) error { <-release; return nil })

	first, err := m.RunJob(ctx, "slow", Request{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	var busy *RunningError
	if _, err := m.RunJob(ctx, "slow", Request{Trigger: TriggerManual}); !errors.As(err, &busy) || busy.RunID != first.ID {
		t.Fatalf("second start: %v", err)
	}
	j, _ := m.Job("slow")
	if cur, ok, err := j.Current(ctx); err != nil || !ok || cur != first.ID {
		t.Fatalf("current: %q %v %v", cur, ok, err)
	}
	if _, err := m.Cancel(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, m, first.ID); run.Status != StatusCancelled {
		t.Fatalf("cancelled run: %+v", run)
	}
	if _, err := m.Cancel(ctx, first.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("cancel a finished run: %v", err)
	}

	s, err := m.RunJob(ctx, "stubborn", Request{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Cancel(ctx, s.ID); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("cancel a non-cancellable run: %v", err)
	}
	close(release)
	wait(t, m, s.ID)
	if _, err := m.GetRun(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
	if _, err := m.RunJob(ctx, "nope", Request{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown job: %v", err)
	}
}

func TestParams(t *testing.T) {
	t.Parallel()
	def := Def{ID: "p", Params: []Param{
		{Name: "force", Type: ParamBool, Default: "false"},
		{Name: "limit", Type: ParamInt},
		{Name: "who", Type: ParamString, Required: true},
		{Name: "mode", Type: ParamString, Choices: []string{"fast", "full"}},
		{Name: "kinds", Type: ParamStringList, Choices: []string{"dump", "delta"}},
	}}
	got, err := Resolve(def, map[string]string{"who": "me", "limit": "5"})
	if err != nil || got["force"] != "false" || got["limit"] != "5" || got["who"] != "me" {
		t.Fatalf("resolve: %v %v", got, err)
	}
	if _, err := Resolve(def, map[string]string{"who": "me", "mode": "full", "kinds": "dump, delta"}); err != nil {
		t.Fatalf("choices: %v", err)
	}
	for name, params := range map[string]map[string]string{
		"unknown":    {"who": "me", "color": "red"},
		"bad int":    {"who": "me", "limit": "lots"},
		"bad bool":   {"who": "me", "force": "maybe"},
		"bad choice": {"who": "me", "mode": "slow"},
		"bad list":   {"who": "me", "kinds": "dump,lookup"},
		"missing":    {},
	} {
		if _, err := Resolve(def, params); !errors.Is(err, ErrInvalidParam) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if l := List(map[string]string{"ids": "a, b,,c"}, "ids"); strings.Join(l, "|") != "a|b|c" {
		t.Errorf("list: %v", l)
	}
}

func TestRetention(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newManager(t, storetest.New(t), 2)
	m.Add(Def{ID: "quick"}, func(context.Context, *Recorder) error { return nil })
	var ids []string
	for range 4 {
		s, err := m.RunJob(ctx, "quick", Request{Trigger: TriggerManual})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, m, s.ID)
		m.Wait() // the run is pruned after it is recorded
		ids = append(ids, s.ID)
	}
	j, _ := m.Job("quick")
	runs, err := j.Runs(ctx, "", 10, 0)
	if err != nil || len(runs) != 2 || runs[0].ID != ids[3] || runs[1].ID != ids[2] {
		t.Fatalf("kept runs: %+v %v", runs, err)
	}
}

func TestInterruptedRunsAndOtherProcesses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := storetest.New(t)
	block := make(chan struct{})
	defer close(block)
	body := func(ctx context.Context, _ *Recorder) error {
		select {
		case <-ctx.Done():
		case <-block:
		}
		return nil
	}

	// A run a crashed meta left behind: nobody holds its lock.
	dead := uuid.Must(uuid.NewV7())
	if _, err := st.Pool.Exec(ctx, `INSERT INTO job_runs (id, job_id, trigger) VALUES ($1, 'shared', 'manual')`, dead); err != nil {
		t.Fatal(err)
	}
	a := newManager(t, st, 100)
	a.Add(Def{ID: "shared"}, body)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	if run, err := a.GetRun(ctx, dead.String()); err != nil || run.Status != StatusFailed || !strings.HasPrefix(run.Message, "interrupted") {
		t.Fatalf("left-over run: %+v %v", run, err)
	}

	// A live run in process a is busy for process b, and b starting up
	// doesn't mark it interrupted.
	live, err := a.RunJob(ctx, "shared", Request{Trigger: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	b := newManager(t, st, 100)
	b.Add(Def{ID: "shared"}, body)
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	var busy *RunningError
	if _, err := b.RunJob(ctx, "shared", Request{Trigger: TriggerManual}); !errors.As(err, &busy) || !busy.Elsewhere || busy.RunID != live.ID {
		t.Fatalf("start in another process: %v", err)
	}
	if run, _ := b.GetRun(ctx, live.ID); run.Status != StatusRunning {
		t.Fatalf("live run after another process started: %+v", run)
	}
	jb, _ := b.Job("shared")
	if cur, ok, _ := jb.Current(ctx); !ok || cur != live.ID {
		t.Fatalf("current seen from the other process: %q %v", cur, ok)
	}
}

func TestScheduledRunWhileBusyIsSkipped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newManager(t, storetest.New(t), 100)
	release := make(chan struct{})
	m.Add(Def{ID: "tick", Schedule: Schedule{Interval: time.Hour, Enabled: true, Source: "test"}}, func(context.Context, *Recorder) error {
		<-release
		return nil
	})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	if next, ok := m.NextRun("tick"); !ok || time.Until(next) < 59*time.Minute {
		t.Fatalf("next run: %v %v", next, ok)
	}
	m.scheduled("tick") // the schedule fires
	m.scheduled("tick") // and fires again while the first run goes on
	close(release)
	m.Wait()
	j, _ := m.Job("tick")
	runs, err := j.Runs(ctx, "", 10, 0)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	statuses := runs[0].Status + "," + runs[1].Status
	if !strings.Contains(statuses, StatusSkipped) || !strings.Contains(statuses, StatusSucceeded) {
		t.Fatalf("statuses: %s", statuses)
	}
	for _, r := range runs {
		if r.Trigger != TriggerSchedule {
			t.Fatalf("trigger: %+v", r)
		}
	}
}

func TestInvalidJobIDPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("a job ID with ':' was accepted")
		}
	}()
	New(nil, slog.New(slog.DiscardHandler), 1).Add(Def{ID: "sync:x:dump"}, nil)
}
