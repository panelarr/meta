// SPDX-License-Identifier: GPL-3.0-or-later

package ingest_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/duplicates"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/ingest"
	"github.com/panelarr/meta/internal/store/storetest"
	"github.com/panelarr/meta/sdk/client"
)

// TestDuplicatesScale measures candidate generation on a synthetic
// catalogue with injected near-duplicates (1 in 100 records): the time of
// a first full run and of an incremental one, and how many of the injected
// pairs it finds. It runs with META_BENCH_DUPLICATES=1 and META_SCALE set to
// the number of records, e.g. 300000.
func TestDuplicatesScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("META_SCALE"))
	if n == 0 || os.Getenv("META_BENCH_DUPLICATES") != "1" {
		t.Skip("set META_BENCH_DUPLICATES=1 and META_SCALE to run")
	}
	ctx := context.Background()
	st := storetest.New(t)
	gen := generator{n: n, light: true, dupPct: 1}
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "gen", Remote: &config.Remote{URL: "x"}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := host.New(cfg, st, log, func(context.Context, config.Provider, *slog.Logger) (client.Client, error) {
		return client.InProcess(gen), nil
	})
	w := catalog.NewWriter(st, catalog.Options{Priority: catalog.Priority{Fallback: []string{"gen"}}, DisplayLanguage: "en"})
	start := time.Now()
	if _, err := ingest.NewRunner(st, h, w, log, 1000).Run(ctx, "gen", "dump", ingest.TriggerManual, true); err != nil {
		t.Fatal(err)
	}
	var entries, injected int
	_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM entries WHERE state = 'active'`).Scan(&entries)
	_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM provider_entries WHERE external_id LIKE 'dup-%'`).Scan(&injected)
	t.Logf("ingested %d active entries (%d injected duplicates) in %s", entries, injected, time.Since(start).Round(time.Second))
	_, _ = st.Pool.Exec(ctx, `ANALYZE`)

	f := duplicates.NewFinder(st.Pool, duplicates.FinderOptions{})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	start = time.Now()
	last := time.Now()
	rep, err := f.Run(runCtx, time.Time{}, uuid.New(), func(done, total int64) {
		if time.Since(last) > 30*time.Second {
			last = time.Now()
			t.Logf("  %d of %d entries, %s", done, total, time.Since(start).Round(time.Second))
		}
		if stop := os.Getenv("META_BENCH_STOP"); stop != "" {
			if n, _ := strconv.ParseInt(stop, 10, 64); done >= n {
				cancel()
			}
		}
	})
	full := time.Since(start)
	if err != nil {
		t.Logf("stopped after %d entries in %s: %v; took %v", rep.Entries, full.Round(time.Second), err, rep.Took)
		return
	}

	var found, open int
	_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM duplicate_candidates`).Scan(&open)
	_ = st.Pool.QueryRow(ctx, `
		SELECT count(*) FROM duplicate_candidates c
		JOIN provider_entries d ON d.entry_id IN (c.entry_a, c.entry_b) AND d.external_id LIKE 'dup-%'
		JOIN provider_entries o ON o.entry_id IN (c.entry_a, c.entry_b) AND o.external_id = substr(d.external_id, 5)`).Scan(&found)
	t.Logf("full run: %d entries, %d pairs compared, %d candidates in %s; %d of %d injected pairs found (recall %.0f%%), precision %.0f%%; skipped %v; took %v",
		rep.Entries, rep.Compared, open, full.Round(time.Second), found, injected,
		100*float64(found)/float64(max(injected, 1)), 100*float64(found)/float64(max(open, 1)), rep.Skipped, rep.Took)

	// An incremental run after 1% of the entries changed.
	since := time.Now()
	if _, err := st.Pool.Exec(ctx, `UPDATE entries SET updated_at = now() WHERE state = 'active' AND random() < 0.01`); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	rep, err = f.Run(ctx, since.Add(-time.Second), uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("incremental run: %d entries in %s", rep.Entries, time.Since(start).Round(time.Millisecond))
}
