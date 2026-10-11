// SPDX-License-Identifier: GPL-3.0-or-later

package search_test

import (
	"context"
	"testing"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/search"
	"github.com/panelarr/meta/internal/search/enginetest"
	"github.com/panelarr/meta/internal/search/searchtest"
)

// engines are the engine backends the conformance suite runs on.
var engines = []struct {
	name string
	new  func(t *testing.T) search.Engine
}{
	{"typesense", func(t *testing.T) search.Engine {
		u, key := enginetest.Typesense(t)
		return search.NewTypesense(u, key)
	}},
	{"meilisearch", func(t *testing.T) search.Engine {
		u, key := enginetest.Meilisearch(t)
		return search.NewMeilisearch(u, key)
	}},
}

func TestEngineConformance(t *testing.T) {
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			t.Parallel()
			eng := e.new(t)
			ctx := context.Background()
			f := searchtest.Seed(t, true)
			x := search.NewExternal(eng, search.NewPostgres(f.Store.Pool), f.Store.Pool,
				search.ExternalOptions{Prefix: enginetest.Prefix(), Browse: search.BrowseEngine})
			if _, err := x.RebuildWith(ctx, nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.DeleteIndex(context.Background(), x.Current()) })
			searchtest.Run(t, f, x, e.name)
		})
	}
}

// TestEngineChangeFeed runs the outbox and a second rebuild against each
// engine: changed, merged and removed entries reach it, and the swap
// leaves one index.
func TestEngineChangeFeed(t *testing.T) {
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			t.Parallel()
			eng := e.new(t)
			ctx := context.Background()
			f := searchtest.Seed(t, true)
			x := search.NewExternal(eng, search.NewPostgres(f.Store.Pool), f.Store.Pool, search.ExternalOptions{Prefix: enginetest.Prefix()})
			if _, err := x.RebuildWith(ctx, nil); err != nil {
				t.Fatal(err)
			}
			first := x.Current()

			recs := searchtest.Corpus()
			recs[1].Titles[0].Text = "Lantern Keeper Returns"
			change(t, f, catalog.Batch{Upserts: []*metadatav1.ProviderEntry{recs[1]}})
			change(t, f, catalog.Batch{Merges: []catalog.Merge{{Merged: "4", Into: "1"}}, Removals: []string{"3"}})
			ix := search.NewIndexer(x, search.IndexerOptions{})
			if n, err := ix.Drain(ctx, nil); err != nil || n == 0 {
				t.Fatalf("drain: %d %v", n, err)
			}
			expectHits := func(text string, want int) {
				t.Helper()
				res, err := x.Query(ctx, search.Query{Text: text})
				if err != nil || res.Backend != e.name || len(res.Hits) != want {
					t.Errorf("%q: %d hits from %s (%v), want %d", text, len(res.Hits), res.Backend, err, want)
				}
			}
			expectHits("returns", 1)
			expectHits("jade river", 0)
			expectHits("quiet orbit", 0)

			if _, err := x.RebuildWith(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if n, err := eng.Count(ctx, x.Current()); err != nil || n != 2 {
				t.Errorf("documents after the second rebuild: %d %v", n, err)
			}
			if x.Current() != first {
				if ok, _ := eng.HasIndex(ctx, first); ok {
					t.Error("the first index wasn't dropped")
				}
			}
			expectHits("returns", 1)
			t.Cleanup(func() { _ = eng.DeleteIndex(context.Background(), x.Current()) })
		})
	}
}
