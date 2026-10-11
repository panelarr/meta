// SPDX-License-Identifier: GPL-3.0-or-later

// Package searchtest is the conformance suite every search backend must
// pass: the same made-up catalogue, the same filters, sorts, paging and
// multilingual searches, run against PostgreSQL and each engine.
package searchtest

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/search"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/storetest"
)

// Provider is the made-up provider of the corpus.
const Provider = "conf"

func p[T any](v T) *T { return &v }

func title(text, lang string, primary bool) *metadatav1.Title {
	kind := metadatav1.TitleKind_TITLE_KIND_ALTERNATIVE
	if primary {
		kind = metadatav1.TitleKind_TITLE_KIND_OFFICIAL
	}
	return &metadatav1.Title{Text: text, Language: lang, IsPrimary: primary, Kind: kind}
}

func genre(name string) *metadatav1.TagAssignment {
	return &metadatav1.TagAssignment{Tag: &metadatav1.Tag{Name: name, IsGenre: true}}
}

func tag(name string, path ...string) *metadatav1.TagAssignment {
	return &metadatav1.TagAssignment{Tag: &metadatav1.Tag{Name: name, Path: path}}
}

func author(name string) *metadatav1.Credit {
	return &metadatav1.Credit{Person: &metadatav1.Person{Name: name}, Role: metadatav1.PersonRole_PERSON_ROLE_AUTHOR}
}

func record(id string, mod func(e *metadatav1.ProviderEntry)) *metadatav1.ProviderEntry {
	e := &metadatav1.ProviderEntry{
		Ref:   &metadatav1.ExternalId{Provider: Provider, Id: id},
		State: metadatav1.EntryState_ENTRY_STATE_ACTIVE,
	}
	mod(e)
	return e
}

// Corpus is the made-up catalogue. Ranks, ratings, years and titles are
// all distinct, so every sort order is fully determined.
func Corpus() []*metadatav1.ProviderEntry {
	return []*metadatav1.ProviderEntry{
		record("1", func(e *metadatav1.ProviderEntry) {
			e.Titles = []*metadatav1.Title{title("Attack of the Giants", "en", true), title("進撃の巨人", "ja", true), title("Shingeki no Kyojin", "ja-Latn", false)}
			e.Type, e.Status, e.ContentRating = metadatav1.EntryType_ENTRY_TYPE_MANGA, metadatav1.EntryStatus_ENTRY_STATUS_COMPLETED, metadatav1.ContentRating_CONTENT_RATING_SUGGESTIVE
			e.OriginalLanguage = "ja"
			e.Dates = &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 2009}}
			e.Stats = &metadatav1.Stats{Rating: p(85.0), PopularityRank: p(int64(1))}
			e.Tags = []*metadatav1.TagAssignment{genre("Action"), tag("Titans"), tag("Military")}
			e.Credits = []*metadatav1.Credit{author("Hajime Fictional")}
		}),
		record("2", func(e *metadatav1.ProviderEntry) {
			e.Titles = []*metadatav1.Title{title("Lantern Keeper", "en", true), title("등불지기", "ko", true)}
			e.Type, e.Status, e.ContentRating = metadatav1.EntryType_ENTRY_TYPE_MANHWA, metadatav1.EntryStatus_ENTRY_STATUS_RELEASING, metadatav1.ContentRating_CONTENT_RATING_SAFE
			e.OriginalLanguage = "ko"
			e.Dates = &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 2018}}
			e.Stats = &metadatav1.Stats{Rating: p(91.0), PopularityRank: p(int64(5))}
			e.Tags = []*metadatav1.TagAssignment{genre("Fantasy"), tag("Titans")}
		}),
		record("3", func(e *metadatav1.ProviderEntry) {
			e.Titles = []*metadatav1.Title{title("Quiet Orbit", "en", true)}
			e.Type, e.Status, e.ContentRating = metadatav1.EntryType_ENTRY_TYPE_MANGA, metadatav1.EntryStatus_ENTRY_STATUS_HIATUS, metadatav1.ContentRating_CONTENT_RATING_EROTIC
			e.OriginalLanguage = "ja"
			e.Dates = &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 2001}}
			e.Stats = &metadatav1.Stats{Rating: p(60.0), PopularityRank: p(int64(3))}
			// A tag named like a genre is still a tag.
			e.Tags = []*metadatav1.TagAssignment{genre("Action"), tag("Space", "Settings"), tag("Fantasy", "Settings")}
		}),
		record("4", func(e *metadatav1.ProviderEntry) {
			e.Titles = []*metadatav1.Title{title("Jade River Chronicle", "en", true), title("翡翠江传", "zh", true), title("Feicui Jiang Zhuan", "zh-Latn", false)}
			e.Type, e.Status, e.ContentRating = metadatav1.EntryType_ENTRY_TYPE_MANHUA, metadatav1.EntryStatus_ENTRY_STATUS_RELEASING, metadatav1.ContentRating_CONTENT_RATING_SAFE
			e.OriginalLanguage = "zh"
			e.Dates = &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 2015}}
			e.Stats = &metadatav1.Stats{Rating: p(78.0), PopularityRank: p(int64(2))}
			e.Tags = []*metadatav1.TagAssignment{genre("Fantasy"), genre("Historical")}
			e.Credits = []*metadatav1.Credit{author("Lin Imaginary")}
		}),
		record("5", func(e *metadatav1.ProviderEntry) {
			e.Titles = []*metadatav1.Title{title("Old Copy of Orbit", "en", true)}
		}),
		record("6", func(e *metadatav1.ProviderEntry) {
			e.Titles = []*metadatav1.Title{title("Withdrawn Lantern", "en", true)}
		}),
	}
}

// Fixture is a database with the corpus.
type Fixture struct {
	Store  *store.Store
	Writer *catalog.Writer
	// IDs by title of the active entries.
	IDs map[string]uuid.UUID
}

// Seed writes the corpus to a fresh database: record 5 is merged into 3
// and record 6 removed, so their entries must never be found. With outbox
// set, the writer records changes for an engine.
func Seed(t *testing.T, outbox bool) *Fixture {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	w := catalog.NewWriter(st, catalog.Options{Priority: catalog.Priority{Fallback: []string{Provider}}, DisplayLanguage: "en", SearchOutbox: outbox})
	if _, err := w.Apply(ctx, catalog.Batch{Provider: Provider, Upserts: Corpus()}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Apply(ctx, catalog.Batch{Provider: Provider, Merges: []catalog.Merge{{Merged: "5", Into: "3"}}, Removals: []string{"6"}}); err != nil {
		t.Fatal(err)
	}
	f := &Fixture{Store: st, Writer: w, IDs: map[string]uuid.UUID{}}
	rows, err := st.Pool.Query(ctx, `SELECT id, title FROM entries WHERE state = 'active'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			t.Fatal(err)
		}
		f.IDs[title] = id
	}
	if len(f.IDs) != 4 {
		t.Fatalf("active entries: %v", f.IDs)
	}
	return f
}

// Titles are the display titles of hits, in order.
func (f *Fixture) Titles(t *testing.T, hits []search.Hit) []string {
	t.Helper()
	byID := map[uuid.UUID]string{}
	for title, id := range f.IDs {
		byID[id] = title
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		title, ok := byID[h.ID]
		if !ok {
			e, err := f.Store.GetEntry(context.Background(), h.ID)
			if err != nil {
				t.Fatalf("hit %s: %v", h.ID, err)
			}
			title = e.Title + " (" + e.State + ")"
		}
		out = append(out, title)
	}
	return out
}

// TagKey returns the key of a tag of the corpus.
func (f *Fixture) TagKey(t *testing.T, name string, genre bool) string {
	t.Helper()
	var key string
	if err := f.Store.Pool.QueryRow(context.Background(), `SELECT key FROM tags WHERE name = $1 AND is_genre = $2`, name, genre).Scan(&key); err == nil {
		return key
	}
	t.Fatalf("no tag %q (genre %v)", name, genre)
	return ""
}

var (
	attack  = "Attack of the Giants"
	lantern = "Lantern Keeper"
	quiet   = "Quiet Orbit"
	jade    = "Jade River Chronicle"
)

// Run runs the suite against the index newIndex returns for the fixture.
// backend is the name results must report, so a silent fallback to
// PostgreSQL fails the suite.
func Run(t *testing.T, f *Fixture, idx search.Index, backend string) {
	ctx := context.Background()
	query := func(t *testing.T, q search.Query) search.Result {
		t.Helper()
		res, err := idx.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if res.Backend != backend {
			t.Fatalf("answered by %q, want %q", res.Backend, backend)
		}
		return res
	}
	expect := func(t *testing.T, q search.Query, want ...string) {
		t.Helper()
		res := query(t, q)
		if got := f.Titles(t, res.Hits); !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
		if res.Total != int64(len(want)) && q.Limit == 0 && q.Offset == 0 {
			t.Errorf("total %d, want %d", res.Total, len(want))
		}
	}

	t.Run("browse", func(t *testing.T) {
		fantasyTag := f.TagKey(t, "Fantasy", false)
		fantasyGenre := f.TagKey(t, "Fantasy", true)
		cases := []struct {
			name string
			q    search.Query
			want []string
		}{
			{"popularity", search.Query{}, []string{attack, jade, quiet, lantern}},
			{"popularity reversed", search.Query{Sort: search.SortPopularity, Reverse: true}, []string{lantern, quiet, jade, attack}},
			{"rating", search.Query{Sort: search.SortRating}, []string{lantern, attack, jade, quiet}},
			{"year reversed", search.Query{Sort: search.SortYear, Reverse: true}, []string{quiet, attack, jade, lantern}},
			{"title", search.Query{Sort: search.SortTitle}, []string{attack, jade, lantern, quiet}},
			{"type", search.Query{Filter: search.Filter{Types: []string{"manhwa", "manhua"}}}, []string{jade, lantern}},
			{"genre by name", search.Query{Filter: search.Filter{Genres: []string{"action"}}}, []string{attack, quiet}},
			// The tag "Settings › Fantasy" is not the genre.
			{"genre named like a tag", search.Query{Filter: search.Filter{Genres: []string{"Fantasy"}}}, []string{jade, lantern}},
			{"genres any of", search.Query{Filter: search.Filter{Genres: []string{"Historical", fantasyGenre}}}, []string{jade, lantern}},
			{"tag by key", search.Query{Filter: search.Filter{Tags: []string{fantasyTag}}}, []string{quiet}},
			{"tags all of", search.Query{Filter: search.Filter{Tags: []string{"Titans", "Military"}}}, []string{attack}},
			{"exclude tag", search.Query{Filter: search.Filter{ExcludeTags: []string{"Titans"}}}, []string{jade, quiet}},
			{"exclude genre key", search.Query{Filter: search.Filter{ExcludeTags: []string{fantasyGenre}}}, []string{attack, quiet}},
			{"status", search.Query{Filter: search.Filter{Statuses: []string{"releasing", "hiatus"}}}, []string{jade, quiet, lantern}},
			{"content rating", search.Query{Filter: search.Filter{ContentRatings: []string{"safe", "suggestive"}}}, []string{attack, jade, lantern}},
			{"years", search.Query{Filter: search.Filter{YearFrom: 2005, YearTo: 2016}}, []string{attack, jade}},
			{"original language", search.Query{Filter: search.Filter{Languages: []string{"ko", "zh"}}}, []string{jade, lantern}},
			{"title language", search.Query{Filter: search.Filter{TitleLanguages: []string{"ja"}}}, []string{attack}},
			{"romanized title language", search.Query{Filter: search.Filter{TitleLanguages: []string{"zh-latn"}}}, []string{jade}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) { expect(t, c.q, c.want...) })
		}
		t.Run("paging", func(t *testing.T) {
			res := query(t, search.Query{Limit: 2, Offset: 2})
			if got := f.Titles(t, res.Hits); !slices.Equal(got, []string{quiet, lantern}) || res.Total != 4 {
				t.Errorf("page 2: %v total %d", got, res.Total)
			}
		})
	})

	t.Run("search", func(t *testing.T) {
		cases := []struct {
			name, text, want, field string
		}{
			{"exact title", "Attack of the Giants", attack, "title"},
			{"case", "QUIET", quiet, "title"},
			{"romanized", "shingeki", attack, "title"},
			{"japanese substring", "進撃", attack, "title"},
			{"chinese substring", "翡翠", jade, "title"},
			{"korean", "등불지기", lantern, "title"},
			{"pinyin", "feicui", jade, "title"},
			{"typos", "lantenr keepr", lantern, "title"},
			{"person", "hajime fictional", attack, "person"},
			{"person typo", "lin imaginery", jade, "person"}, //nolint:misspell // the typo is the test
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				res := query(t, search.Query{Text: c.text})
				if len(res.Hits) == 0 {
					t.Fatalf("%q: no hits", c.text)
				}
				if got := f.Titles(t, res.Hits[:1]); got[0] != c.want || res.Hits[0].Field != c.field {
					t.Errorf("%q: first %v (%s), want %s (%s); all %v", c.text, got, res.Hits[0].Field, c.want, c.field, f.Titles(t, res.Hits))
				}
			})
		}
		t.Run("nothing", func(t *testing.T) { expect(t, search.Query{Text: "zzzz qqqq"}) })
		t.Run("filtered", func(t *testing.T) {
			expect(t, search.Query{Text: "giants", Filter: search.Filter{Types: []string{"manhwa"}}})
		})
		t.Run("merged and removed are not found", func(t *testing.T) {
			res := query(t, search.Query{Text: "copy"})
			for _, title := range f.Titles(t, res.Hits) {
				if title != quiet { // the merged record's titles now belong to Quiet Orbit
					t.Errorf("found %s", title)
				}
			}
			expect(t, search.Query{Text: "withdrawn"})
		})
		t.Run("relevance then popularity", func(t *testing.T) {
			res := query(t, search.Query{Text: "lantern"})
			if got := f.Titles(t, res.Hits); len(got) == 0 || got[0] != lantern {
				t.Errorf("lantern: %v", got)
			}
		})
	})

	if !idx.Capabilities().Facets {
		return
	}
	t.Run("facets", func(t *testing.T) {
		res := query(t, search.Query{Facets: true, Text: "a"})
		_ = res
		all := query(t, search.Query{Facets: true})
		counts := map[string]map[string]int64{}
		for _, fc := range all.Facets {
			counts[fc.Field] = map[string]int64{}
			for _, v := range fc.Values {
				counts[fc.Field][v.Value] = v.Count
			}
		}
		if counts[search.FacetGenres][f.TagKey(t, "Action", true)] != 2 || counts[search.FacetGenres][f.TagKey(t, "Fantasy", true)] != 2 {
			t.Errorf("genre facet: %v", counts[search.FacetGenres])
		}
		if counts[search.FacetType]["manga"] != 2 || counts[search.FacetType]["manhwa"] != 1 {
			t.Errorf("type facet: %v", counts[search.FacetType])
		}
		if counts[search.FacetOriginalLanguage]["ja"] != 2 || counts[search.FacetTags][f.TagKey(t, "Titans", false)] != 2 {
			t.Errorf("facets: %v", counts)
		}
		filtered := query(t, search.Query{Facets: true, Filter: search.Filter{Types: []string{"manga"}}})
		for _, fc := range filtered.Facets {
			if fc.Field == search.FacetType && (len(fc.Values) != 1 || fc.Values[0].Count != 2) {
				t.Errorf("facets of a filtered query: %v", fc.Values)
			}
		}
	})
}
