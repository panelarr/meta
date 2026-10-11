// SPDX-License-Identifier: GPL-3.0-or-later

package ingest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/ingest"
	"github.com/panelarr/meta/internal/search"
	"github.com/panelarr/meta/internal/search/enginetest"
	"github.com/panelarr/meta/internal/store/storetest"
	"github.com/panelarr/meta/sdk/client"
)

// TestSearchEngineBenchmark compares PostgreSQL, Typesense and Meilisearch
// on a synthetic catalogue: index build time, engine memory and size, and
// query latency. It runs with META_BENCH_ENGINES=1, META_TEST_ENGINES=1 and
// META_SCALE set to the number of records, e.g. 300000.
func TestSearchEngineBenchmark(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("META_SCALE"))
	if n == 0 || os.Getenv("META_BENCH_ENGINES") != "1" {
		t.Skip("set META_BENCH_ENGINES=1 and META_SCALE to run")
	}
	ctx := context.Background()
	st := storetest.New(t)
	gen := generator{n: n, light: true}
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
	var entries int
	_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM entries WHERE state = 'active'`).Scan(&entries)
	t.Logf("ingested %d active entries in %s", entries, time.Since(start).Round(time.Second))

	queries := []struct {
		name string
		q    search.Query
	}{
		{"exact title", search.Query{Text: words(4242*5, 2+4242%4), Limit: 24}},
		{"title fragment", search.Query{Text: words(777*5, 2+777%4)[1:], Limit: 24}},
		{"short word", search.Query{Text: "kari", Limit: 24}},
		{"typo", search.Query{Text: "karri", Limit: 24}},
		{"person", search.Query{Text: "Person 777", Limit: 24}},
		{"text and filters", search.Query{Text: "kari", Limit: 24, Filter: search.Filter{Types: []string{"manga"}, Languages: []string{"ja"}}}},
		{"browse with facets", search.Query{Limit: 24, Facets: true, Filter: search.Filter{Genres: []string{"Tag 3"}}}},
	}
	measure := func(name string, idx search.Index, backend string) {
		for _, c := range queries {
			var times []time.Duration
			var hits, total int64
			for range 40 {
				s := time.Now()
				res, err := idx.Query(ctx, c.q)
				if err != nil {
					t.Fatalf("%s %s: %v", name, c.name, err)
				}
				if res.Backend != backend {
					t.Fatalf("%s %s: answered by %s", name, c.name, res.Backend)
				}
				times = append(times, time.Since(s))
				hits, total = int64(len(res.Hits)), res.Total
			}
			slices.Sort(times)
			t.Logf("%-12s %-20s p50 %6s  p95 %6s  (%d hits of %d)", name, c.name,
				times[len(times)/2].Round(100*time.Microsecond), times[len(times)*95/100].Round(100*time.Microsecond), hits, total)
		}
	}
	measure("postgres", search.NewPostgres(st.Pool), "postgres")

	for _, e := range []struct {
		name  string
		start func(testing.TB) (string, string)
		new   func(u, key string) search.Engine
		stats func(u, key string) string
	}{
		{"typesense", enginetest.Typesense, func(u, k string) search.Engine { return search.NewTypesense(u, k) }, typesenseStats},
		{"meilisearch", enginetest.Meilisearch, func(u, k string) search.Engine { return search.NewMeilisearch(u, k) }, meiliStats},
	} {
		u, key := e.start(t)
		x := search.NewExternal(e.new(u, key), search.NewPostgres(st.Pool), st.Pool,
			search.ExternalOptions{Prefix: enginetest.Prefix(), Browse: search.BrowseEngine, Timeout: 30 * time.Second})
		s := time.Now()
		docs, err := x.RebuildWith(ctx, nil)
		if err != nil {
			t.Fatalf("%s rebuild: %v", e.name, err)
		}
		t.Logf("%s: indexed %d entries in %s; %s", e.name, docs, time.Since(s).Round(time.Second), e.stats(u, key))
		measure(e.name, x, e.name)
		_ = x.Engine().DeleteIndex(ctx, x.Current())
	}
}

func getJSON(url, header, key string) map[string]any {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set(header, key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out
}

func typesenseStats(u, key string) string {
	m := getJSON(u+"/metrics.json", "X-TYPESENSE-API-KEY", key)
	return fmt.Sprintf("memory active %s MB, resident %s MB, system used %s MB",
		mb(m["typesense_memory_active_bytes"]), mb(m["typesense_memory_resident_bytes"]), mb(m["system_memory_used_bytes"]))
}

func meiliStats(u, key string) string {
	m := getJSON(u+"/stats", "Authorization", "Bearer "+key)
	return fmt.Sprintf("database %s MB (used %s MB)", mb(m["databaseSize"]), mb(m["usedDatabaseSize"]))
}

func mb(v any) string {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		f, _ = strconv.ParseFloat(x, 64)
	}
	return strconv.FormatFloat(f/1e6, 'f', 0, 64)
}
