// SPDX-License-Identifier: GPL-3.0-or-later

package search_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/search"
	"github.com/panelarr/meta/internal/search/searchtest"
)

// memEngine is an engine in memory: indexes are maps, the live name is an
// alias, and searches match title and name substrings.
type memEngine struct {
	mu      sync.Mutex
	indexes map[string]map[string]search.Document
	alias   map[string]string
	// fail makes every call fail while set.
	fail atomic.Bool
	// onUpsert runs before each upsert, outside the lock.
	onUpsert func(index string)
}

func newMem() *memEngine {
	return &memEngine{indexes: map[string]map[string]search.Document{}, alias: map[string]string{}}
}

var errDown = errors.New("engine down")

func (m *memEngine) Name() string { return "mem" }

func (m *memEngine) Check(context.Context) error {
	if m.fail.Load() {
		return errDown
	}
	return nil
}

func (m *memEngine) CreateIndex(_ context.Context, name string) error {
	if m.fail.Load() {
		return errDown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.indexes[name] = map[string]search.Document{}
	return nil
}

func (m *memEngine) DeleteIndex(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.indexes, name)
	return nil
}

func (m *memEngine) HasIndex(_ context.Context, name string) (bool, error) {
	if m.fail.Load() {
		return false, errDown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.indexes[name]
	return ok, nil
}

func (m *memEngine) Upsert(_ context.Context, index string, docs []search.Document) error {
	if m.onUpsert != nil {
		m.onUpsert(index)
	}
	if m.fail.Load() {
		return errDown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range docs {
		m.indexes[index][d.ID] = d
	}
	return nil
}

func (m *memEngine) Delete(_ context.Context, index string, ids []string) error {
	if m.fail.Load() {
		return errDown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.indexes[index], id)
	}
	return nil
}

func (m *memEngine) Count(_ context.Context, index string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.indexes[index])), nil
}

func (m *memEngine) Activate(_ context.Context, live, index string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.alias[live]
	m.alias[live] = index
	return index, old, nil
}

func (m *memEngine) Search(_ context.Context, live string, q search.Query) (search.Result, error) {
	if m.fail.Load() {
		return search.Result{}, errDown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var res search.Result
	text := strings.ToLower(q.Text)
	for _, d := range m.indexes[m.alias[live]] {
		all := append(append([]string{d.Title}, d.TitlesJa...), d.TitlesLatn...)
		for _, s := range append(all, d.People...) {
			if strings.Contains(strings.ToLower(s), text) {
				res.Hits = append(res.Hits, search.Hit{ID: uuid.MustParse(d.ID), Matched: s, Field: "title"})
				break
			}
		}
	}
	res.Total = int64(len(res.Hits))
	return res, nil
}

func (m *memEngine) docs(live string) map[string]search.Document {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]search.Document{}
	for k, v := range m.indexes[m.alias[live]] {
		out[k] = v
	}
	return out
}

func pending(t *testing.T, f *searchtest.Fixture) int64 {
	t.Helper()
	var n int64
	if err := f.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM search_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOutboxOnlyWithAnEngine(t *testing.T) {
	t.Parallel()
	if n := pending(t, searchtest.Seed(t, false)); n != 0 {
		t.Fatalf("outbox without an engine: %d rows", n)
	}
	if n := pending(t, searchtest.Seed(t, true)); n == 0 {
		t.Fatal("no outbox rows with an engine")
	}
}

func TestRebuildIndexAndFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := searchtest.Seed(t, true)
	eng := newMem()
	x := search.NewExternal(eng, search.NewPostgres(f.Store.Pool), f.Store.Pool, search.ExternalOptions{})

	// Not built yet: PostgreSQL answers.
	if ok, err := x.Load(ctx); err != nil || ok {
		t.Fatalf("load before a build: %v %v", ok, err)
	}
	if res, err := x.Query(ctx, search.Query{Text: "quiet"}); err != nil || res.Backend != "postgres" || len(res.Hits) != 1 {
		t.Fatalf("query before a build: %+v %v", res, err)
	}

	var progressed int64
	n, err := x.RebuildWith(ctx, func(done, total int64) { progressed = total })
	if err != nil || n != 4 || progressed != 4 {
		t.Fatalf("rebuild: %d %v (progress total %d)", n, err, progressed)
	}
	if pending(t, f) != 0 {
		t.Fatal("the rebuild left outbox rows it covered")
	}
	docs := eng.docs(x.Live())
	if len(docs) != 4 {
		t.Fatalf("documents: %d", len(docs))
	}
	attack := docs[f.IDs["Attack of the Giants"].String()]
	if len(attack.TitlesJa) != 1 || len(attack.TitlesLatn) != 1 || attack.People[0] != "Hajime Fictional" ||
		len(attack.GenreKeys) != 1 || attack.Type != "manga" || *attack.Year != 2009 || *attack.PopularityRank != 1 ||
		!strings.Contains(strings.Join(attack.TitleLanguages, ","), "ja-latn") {
		t.Fatalf("document: %+v", attack)
	}
	quiet := docs[f.IDs["Quiet Orbit"].String()]
	if len(quiet.GenreKeys) != 1 || len(quiet.FacetTags) != 2 || len(quiet.TagKeys) != 3 {
		t.Fatalf("quiet orbit tags: %+v", quiet)
	}
	if res, err := x.Query(ctx, search.Query{Text: "quiet"}); err != nil || res.Backend != "mem" || len(res.Hits) != 1 {
		t.Fatalf("query after the build: %+v %v", res, err)
	}
	// Browsing stays on PostgreSQL unless facets are wanted.
	if res, _ := x.Query(ctx, search.Query{}); res.Backend != "postgres" {
		t.Fatalf("browse: %s", res.Backend)
	}

	// The engine goes down: PostgreSQL answers, and the status says so.
	eng.fail.Store(true)
	res, err := x.Query(ctx, search.Query{Text: "quiet"})
	if err != nil || res.Backend != "postgres" || len(res.Hits) != 1 {
		t.Fatalf("fallback: %+v %v", res, err)
	}
	st, err := x.Status(ctx)
	if err != nil || !st.Degraded || st.LastErr == "" || st.Documents != 4 || !st.Ready {
		t.Fatalf("status while down: %+v %v", st, err)
	}
	eng.fail.Store(false)
	if res, _ := x.Query(ctx, search.Query{Text: "quiet"}); res.Backend != "mem" {
		t.Fatal("no recovery")
	}
	if st, _ := x.Status(ctx); st.Degraded {
		t.Fatal("still degraded")
	}

	// A second rebuild swaps in a new index and drops the old one.
	old := eng.alias[x.Live()]
	if _, err := x.RebuildWith(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if eng.alias[x.Live()] == old {
		t.Fatal("no swap")
	}
	if ok, _ := eng.HasIndex(ctx, old); ok {
		t.Fatal("the old index wasn't dropped")
	}
}

func TestSearchesDuringRebuildUseTheOldIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := searchtest.Seed(t, true)
	eng := newMem()
	x := search.NewExternal(eng, search.NewPostgres(f.Store.Pool), f.Store.Pool, search.ExternalOptions{})
	if _, err := x.RebuildWith(ctx, nil); err != nil {
		t.Fatal(err)
	}
	first := eng.alias[x.Live()]
	var during []string
	eng.onUpsert = func(index string) {
		if index == first {
			return
		}
		// Mid-build: searches still answer, from the old index.
		res, err := x.Query(ctx, search.Query{Text: "lantern"})
		if err != nil || res.Backend != "mem" || len(res.Hits) != 1 {
			t.Errorf("query during the rebuild: %+v %v", res, err)
		}
		during = append(during, eng.alias[x.Live()])
	}
	if _, err := x.RebuildWith(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if len(during) == 0 || during[0] != first {
		t.Fatalf("live index during the build: %v (first %s)", during, first)
	}
}

func change(t *testing.T, f *searchtest.Fixture, b catalog.Batch) {
	t.Helper()
	b.Provider = searchtest.Provider
	if _, err := f.Writer.Apply(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

func TestIndexerAppliesChangesAcrossRestartsAndOutages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := searchtest.Seed(t, true)
	eng := newMem()
	pg := search.NewPostgres(f.Store.Pool)
	x := search.NewExternal(eng, pg, f.Store.Pool, search.ExternalOptions{})
	if _, err := x.RebuildWith(ctx, nil); err != nil {
		t.Fatal(err)
	}

	// A record changes its title, and another becomes merged.
	recs := searchtest.Corpus()
	recs[1].Titles[0].Text = "Lantern Keeper Returns"
	change(t, f, catalog.Batch{Upserts: []*metadatav1.ProviderEntry{recs[1]}})
	change(t, f, catalog.Batch{Merges: []catalog.Merge{{Merged: "4", Into: "1"}}})
	if pending(t, f) == 0 {
		t.Fatal("changes weren't queued")
	}

	// The engine is down: the rows stay.
	eng.fail.Store(true)
	ix := search.NewIndexer(x, search.IndexerOptions{})
	if _, err := ix.Round(ctx); err == nil {
		t.Fatal("a round with the engine down succeeded")
	}
	if pending(t, f) == 0 || ix.Status().LastErr == "" {
		t.Fatal("rows lost while the engine was down")
	}

	// meta restarts: a new backend and indexer pick up the same index and
	// the waiting rows.
	eng.fail.Store(false)
	x2 := search.NewExternal(eng, pg, f.Store.Pool, search.ExternalOptions{})
	if ok, err := x2.Load(ctx); err != nil || !ok {
		t.Fatalf("load after restart: %v %v", ok, err)
	}
	ix2 := search.NewIndexer(x2, search.IndexerOptions{})
	if n, err := ix2.Drain(ctx, nil); err != nil || n == 0 {
		t.Fatalf("drain: %d %v", n, err)
	}
	if pending(t, f) != 0 {
		t.Fatal("rows left after the drain")
	}
	docs := eng.docs(x2.Live())
	if docs[f.IDs["Lantern Keeper"].String()].Title != "Lantern Keeper Returns" {
		t.Errorf("changed title not indexed: %+v", docs[f.IDs["Lantern Keeper"].String()])
	}
	if _, ok := docs[f.IDs["Jade River Chronicle"].String()]; ok {
		t.Error("the merged entry is still indexed")
	}
	if len(docs) != 3 {
		t.Errorf("documents: %d", len(docs))
	}
}

func TestFloodStartsARebuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := searchtest.Seed(t, true)
	eng := newMem()
	x := search.NewExternal(eng, search.NewPostgres(f.Store.Pool), f.Store.Pool, search.ExternalOptions{})

	var floods atomic.Int32
	ix := search.NewIndexer(x, search.IndexerOptions{RebuildThreshold: 2, OnFlood: func() { floods.Add(1) }})
	// No index yet: the indexer asks for a build instead of applying rows.
	if _, err := ix.Round(ctx); err == nil || floods.Load() != 1 {
		t.Fatalf("round without an index: %v, floods %d", err, floods.Load())
	}
	if _, err := x.RebuildWith(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// Many changes at once (a big re-sync): a rebuild, not row by row.
	if _, err := f.Store.Pool.Exec(ctx, `INSERT INTO search_outbox (entry_id) SELECT id FROM entries`); err != nil {
		t.Fatal(err)
	}
	ix2 := search.NewIndexer(x, search.IndexerOptions{RebuildThreshold: 2, OnFlood: func() { floods.Add(1) }})
	if n, err := ix2.Round(ctx); err != nil || n != 0 || floods.Load() != 2 {
		t.Fatalf("flood round: %d %v, floods %d", n, err, floods.Load())
	}
}

func TestSchemaChangeNeedsARebuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := searchtest.Seed(t, false)
	eng := newMem()
	x := search.NewExternal(eng, search.NewPostgres(f.Store.Pool), f.Store.Pool, search.ExternalOptions{})
	if _, err := x.RebuildWith(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Store.Pool.Exec(ctx, `UPDATE search_indexes SET schema_version = schema_version - 1`); err != nil {
		t.Fatal(err)
	}
	if ok, err := x.Load(ctx); err != nil || ok {
		t.Fatalf("an index of an older schema was used: %v %v", ok, err)
	}
}

func TestEngineBackLaterStartsTheFirstBuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := searchtest.Seed(t, false) // nothing waits in the outbox
	eng := newMem()
	eng.fail.Store(true)
	x := search.NewExternal(eng, search.NewPostgres(f.Store.Pool), f.Store.Pool, search.ExternalOptions{})
	var floods atomic.Int32
	ix := search.NewIndexer(x, search.IndexerOptions{OnFlood: func() { floods.Add(1) }})
	if _, err := ix.Round(ctx); err == nil || floods.Load() != 0 {
		t.Fatalf("round with the engine down: %v, floods %d", err, floods.Load())
	}
	eng.fail.Store(false)
	if _, err := ix.Round(ctx); err == nil || floods.Load() != 1 {
		t.Fatalf("round with the engine back: %v, floods %d", err, floods.Load())
	}
}
