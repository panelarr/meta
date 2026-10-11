// SPDX-License-Identifier: GPL-3.0-or-later

package ingest_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/ingest"
	"github.com/panelarr/meta/internal/metrics"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/internal/store/storetest"
	"github.com/panelarr/meta/sdk/client"
	"github.com/panelarr/meta/sdk/plugintest"
)

type env struct {
	metrics *metrics.Metrics
	st      *store.Store
	fake    *plugintest.Fake
	runner  *ingest.Runner
	host    *host.Host
}

func setup(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	fake := &plugintest.Fake{Provider: "fake", SnapshotID: "snap-1", CheckpointEvery: 2}
	fake.Entries = []*metadatav1.ProviderEntry{
		fake.Entry("1", "First Light", "Hajimari"),
		fake.Entry("2", "Second Wind"),
		fake.Entry("3", "Third Act"),
		fake.Entry("4", "Fourth Wall"),
	}
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "fake", Remote: &config.Remote{URL: "inprocess"}, Settings: map[string]any{"greeting": "hi"}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := host.New(cfg, st, log, func(context.Context, config.Provider, *slog.Logger) (client.Client, error) {
		return client.InProcess(fake), nil
	})
	w := catalog.NewWriter(st, catalog.Options{Priority: catalog.Priority{Fallback: cfg.ProviderOrder()}, DisplayLanguage: "en"})
	m := metrics.New()
	h.SetMetrics(m)
	r := ingest.NewRunner(st, h, w, log, 3)
	r.SetMetrics(m)
	return &env{metrics: m, st: st, fake: fake, host: h, runner: r}
}

func (e *env) count(t *testing.T, q string) int {
	t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFullSyncAndUnchangedResync(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := setup(t)

	run, err := e.runner.Run(ctx, "fake", "", ingest.TriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "succeeded" || run.Kind != "dump" || run.Upserts != 4 || run.Changed != 4 || run.SnapshotID != "snap-1" || !run.FullSnapshot {
		t.Fatalf("first run: %+v", run)
	}
	if testutil.ToFloat64(e.metrics.SyncRuns.WithLabelValues("fake", "dump", "succeeded")) != 1 ||
		testutil.ToFloat64(e.metrics.SyncRecords.WithLabelValues("fake", "upsert")) != 4 ||
		testutil.ToFloat64(e.metrics.PluginCalls.WithLabelValues("fake", "sync", "ok")) != 1 {
		t.Fatal("sync metrics not recorded")
	}
	if n := e.count(t, `SELECT count(*) FROM entries WHERE state = 'active'`); n != 4 {
		t.Fatalf("entries: %d", n)
	}
	cur, err := e.st.GetSyncCursor(ctx, db.GetSyncCursorParams{Provider: "fake", Kind: "dump"})
	if err != nil || cur.Cursor != "snap-1" {
		t.Fatalf("cursor: %+v %v", cur, err)
	}
	prov, err := e.st.GetProvider(ctx, "fake")
	if err != nil || prov.Health != "ok" || len(prov.Info) == 0 {
		t.Fatalf("provider row: %+v %v", prov, err)
	}

	// Same snapshot again: the plugin says unchanged, nothing is written.
	run, err = e.runner.Run(ctx, "fake", "dump", ingest.TriggerManual, false)
	if err != nil || run.Status != "succeeded" || run.Upserts != 0 {
		t.Fatalf("unchanged run: %+v %v", run, err)
	}

	// Forced: everything arrives again, nothing changes.
	var before time.Time
	_ = e.st.Pool.QueryRow(ctx, `SELECT max(updated_at) FROM entries`).Scan(&before)
	run, err = e.runner.Run(ctx, "fake", "dump", ingest.TriggerManual, true)
	if err != nil || run.Upserts != 4 || run.Changed != 0 || run.MarkedRemoved != 0 {
		t.Fatalf("forced run: %+v %v", run, err)
	}
	var after time.Time
	_ = e.st.Pool.QueryRow(ctx, `SELECT max(updated_at) FROM entries`).Scan(&after)
	if !after.Equal(before) {
		t.Fatal("an unchanged re-sync rewrote entries")
	}
}

func TestSnapshotMarksMissingRemovedAndMerges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := setup(t)
	if _, err := e.runner.Run(ctx, "fake", "dump", ingest.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
	e.fake.Lock()
	e.fake.SnapshotID = "snap-2"
	e.fake.Entries = e.fake.Entries[:3] // 4 is gone
	e.fake.Merges = []plugintest.Merge{{Merged: "3", Into: "1"}}
	e.fake.Unlock()

	run, err := e.runner.Run(ctx, "fake", "dump", ingest.TriggerSchedule, false)
	if err != nil {
		t.Fatal(err)
	}
	if run.MarkedRemoved != 1 || run.Merges != 1 || run.Trigger != "schedule" {
		t.Fatalf("run: %+v", run)
	}
	if n := e.count(t, `SELECT count(*) FROM entries WHERE state = 'removed'`); n != 1 {
		t.Fatalf("removed entries: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM entries WHERE state = 'merged'`); n != 1 {
		t.Fatalf("merged entries: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM entry_redirects`); n != 1 {
		t.Fatalf("redirects: %d", n)
	}
}

func TestFailedSyncKeepsCommittedBatches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := setup(t)
	e.fake.FailAfter = 3

	run, err := e.runner.Run(ctx, "fake", "dump", ingest.TriggerManual, false)
	if err == nil || run.Status != "failed" || run.Error == "" {
		t.Fatalf("want a failed run: %+v %v", run, err)
	}
	// Records 1-3 arrived before the failure and are kept; nothing is
	// marked removed after a failed snapshot.
	if n := e.count(t, `SELECT count(*) FROM provider_entries`); n != 3 {
		t.Fatalf("kept records: %d", n)
	}
	if run.MarkedRemoved != 0 {
		t.Fatal("a failed snapshot marked records removed")
	}
	cur, err := e.st.GetSyncCursor(ctx, db.GetSyncCursorParams{Provider: "fake", Kind: "dump"})
	if err != nil || cur.Cursor != "2" {
		t.Fatalf("cursor after failure should be the last checkpoint: %+v %v", cur, err)
	}
}

func TestDiskFullSyncFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := setup(t)
	// The database fills up when the third record is written.
	for _, q := range []string{
		`CREATE FUNCTION fill_disk() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.external_id = '3' THEN
				RAISE EXCEPTION 'could not extend file "base/1/2": No space left on device' USING ERRCODE = '53100';
			END IF;
			RETURN NEW;
		END $$`,
		`CREATE TRIGGER fill_disk BEFORE INSERT ON provider_entries FOR EACH ROW EXECUTE FUNCTION fill_disk()`,
	} {
		if _, err := e.st.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	run, err := e.runner.Run(ctx, "fake", "dump", ingest.TriggerManual, false)
	if !errors.Is(err, store.ErrOutOfSpace) {
		t.Fatalf("err = %v, want out of space", err)
	}
	if run.Status != "failed" || !strings.HasPrefix(run.Error, "database out of space: ") {
		t.Fatalf("run: status %q error %q", run.Status, run.Error)
	}
	if n := e.count(t, `SELECT count(*) FROM provider_entries`); n != 2 {
		t.Fatalf("kept records: %d", n)
	}
}

func TestOneRunPerProvider(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := setup(t)
	e.fake.SyncDelay = 100 * time.Millisecond

	first, err := e.runner.Start(ctx, "fake", "dump", ingest.TriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.runner.Start(ctx, "fake", "dump", ingest.TriggerManual, false); !errors.Is(err, ingest.ErrRunning) {
		t.Fatalf("second start: %v", err)
	}
	if id, ok := e.runner.Running("fake"); !ok || id != first.ID {
		t.Fatal("running run not reported")
	}
	cancel()
	e.runner.Wait()
	got, err := e.st.GetSyncRun(context.Background(), first.ID)
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("cancelled run: %+v %v", got, err)
	}
}

func TestUnknownProvider(t *testing.T) {
	t.Parallel()
	e := setup(t)
	if _, err := e.runner.Run(context.Background(), "nope", "dump", ingest.TriggerManual, false); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestScheduler(t *testing.T) {
	t.Parallel()
	e := setup(t)
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "fake", Remote: &config.Remote{URL: "x"}, Schedules: map[string]string{"dump": "@every 1s"}}}
	s, err := ingest.NewScheduler(context.Background(), cfg, e.runner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	defer s.Stop()
	deadline := time.Now().Add(10 * time.Second)
	for e.fake.Syncs() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("scheduled sync never ran")
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.Stop()
	e.runner.Wait()
}

func TestOneRunPerProviderAcrossProcesses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := setup(t)
	// Another process holds the provider's sync lock.
	conn, err := e.st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('meta-sync:' || $1))`, "fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.runner.Run(ctx, "fake", "dump", ingest.TriggerManual, false); !errors.Is(err, ingest.ErrRunning) {
		t.Fatalf("want ErrRunning while another process syncs, got %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('meta-sync:' || $1))`, "fake"); err != nil {
		t.Fatal(err)
	}
	if run, err := e.runner.Run(ctx, "fake", "dump", ingest.TriggerManual, false); err != nil || run.Status != "succeeded" {
		t.Fatalf("after unlock: %+v %v", run, err)
	}
}
