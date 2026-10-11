// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/api"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/ingest"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/internal/store/storetest"
	"github.com/panelarr/meta/sdk/client"
	"github.com/panelarr/meta/sdk/plugintest"
)

type fixture struct {
	st     *store.Store
	srv    *httptest.Server
	meta   metav1connect.MetaServiceClient
	fake   *plugintest.Fake
	writer *catalog.Writer
}

func p[T any](v T) *T { return &v }

func entry(f *plugintest.Fake, id string, mod func(*metadatav1.ProviderEntry)) *metadatav1.ProviderEntry {
	e := f.Entry(id, "")
	e.Titles = nil
	mod(e)
	return e
}

func tag(name string, genre bool) *metadatav1.TagAssignment {
	return &metadatav1.TagAssignment{Tag: &metadatav1.Tag{Name: name, IsGenre: genre}}
}

func setup(t *testing.T, apiKeys ...string) *fixture {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	fake := &plugintest.Fake{Provider: "fake", SnapshotID: "s1"}
	title := func(text, lang string, primary bool) *metadatav1.Title {
		return &metadatav1.Title{Text: text, Language: lang, IsPrimary: primary}
	}
	fake.Entries = []*metadatav1.ProviderEntry{
		entry(fake, "1", func(e *metadatav1.ProviderEntry) {
			e.Titles = []*metadatav1.Title{title("Attack of the Giants", "en", true), title("進撃の巨人", "ja", true), title("Shingeki no Kyojin", "ja-Latn", false)}
			e.Type = metadatav1.EntryType_ENTRY_TYPE_MANGA
			e.Status = metadatav1.EntryStatus_ENTRY_STATUS_COMPLETED
			e.ContentRating = metadatav1.ContentRating_CONTENT_RATING_SUGGESTIVE
			e.OriginalLanguage = "ja"
			e.Dates = &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 2009}}
			e.Stats = &metadatav1.Stats{Rating: p(85.0), PopularityRank: p(int64(1))}
			e.Tags = []*metadatav1.TagAssignment{tag("Action", true), tag("Titans", false), tag("Military", false)}
			e.Credits = []*metadatav1.Credit{{Person: &metadatav1.Person{Name: "Hajime Fictional"}, Role: metadatav1.PersonRole_PERSON_ROLE_AUTHOR}}
			e.ExternalIds = []*metadatav1.ExternalId{{Provider: "anilist", Id: "53390"}}
			e.Images = []*metadatav1.Image{{Kind: metadatav1.ImageKind_IMAGE_KIND_COVER, Url: "https://img.example/1.jpg", Blurhash: "LEHV6n"}}
			e.Relations = []*metadatav1.Relation{{Type: metadatav1.RelationType_RELATION_TYPE_SPIN_OFF, Target: &metadatav1.ExternalId{Provider: "fake", Id: "2"}}}
			e.Descriptions = []*metadatav1.Description{{Language: "en", Text: "Walls and giants."}}
			e.Attribution = &metadatav1.Attribution{ProviderName: "Fake", License: "CC0-1.0"}
		}),
		entry(fake, "2", func(e *metadatav1.ProviderEntry) {
			e.Titles = []*metadatav1.Title{title("Lantern Keeper", "en", true)}
			e.Type = metadatav1.EntryType_ENTRY_TYPE_MANHWA
			e.Status = metadatav1.EntryStatus_ENTRY_STATUS_RELEASING
			e.ContentRating = metadatav1.ContentRating_CONTENT_RATING_SAFE
			e.OriginalLanguage = "ko"
			e.Dates = &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 2018}}
			e.Stats = &metadatav1.Stats{Rating: p(91.0), PopularityRank: p(int64(5))}
			e.Tags = []*metadatav1.TagAssignment{tag("Fantasy", true), tag("Titans", false)}
		}),
		entry(fake, "3", func(e *metadatav1.ProviderEntry) {
			e.Titles = []*metadatav1.Title{title("Quiet Orbit", "en", true)}
			e.Type = metadatav1.EntryType_ENTRY_TYPE_MANGA
			e.Status = metadatav1.EntryStatus_ENTRY_STATUS_HIATUS
			e.ContentRating = metadatav1.ContentRating_CONTENT_RATING_EROTIC
			e.OriginalLanguage = "ja"
			e.Dates = &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 2001}}
			e.Stats = &metadatav1.Stats{Rating: p(60.0), PopularityRank: p(int64(3))}
			e.Tags = []*metadatav1.TagAssignment{tag("Action", true), tag("Space", false)}
		}),
		entry(fake, "4", func(e *metadatav1.ProviderEntry) {
			e.Titles = []*metadatav1.Title{title("Old Copy", "en", true)}
		}),
	}
	fake.Merges = []plugintest.Merge{{Merged: "4", Into: "3"}}

	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "fake", Remote: &config.Remote{URL: "x"}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := host.New(cfg, st, log, func(context.Context, config.Provider, *slog.Logger) (client.Client, error) {
		return client.InProcess(fake), nil
	})
	w := catalog.NewWriter(st, catalog.Options{Priority: catalog.Priority{Fallback: []string{"fake"}}, DisplayLanguage: "en"})
	r := ingest.NewRunner(st, h, w, log, 100)

	// Sync once without the merge, so entry 4 exists on its own, then
	// again with it, so its entry is retired behind a redirect.
	merges := fake.Merges
	fake.Merges = nil
	if _, err := r.Run(ctx, "fake", "dump", ingest.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
	fake.Lock()
	fake.Merges = merges
	fake.SnapshotID = "s2"
	fake.Unlock()
	if _, err := r.Run(ctx, "fake", "dump", ingest.TriggerManual, false); err != nil {
		t.Fatal(err)
	}

	srv, err := api.New(
		api.NewMeta(st, h, w, api.MetaOptions{DisplayLanguage: "en", Log: log}),
		api.NewAdmin(ctx, st, h, r, w),
		api.Options{APIKeys: apiKeys, Log: log},
	)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return &fixture{st: st, srv: hs, meta: metav1connect.NewMetaServiceClient(hs.Client(), hs.URL), fake: fake, writer: w}
}

func (f *fixture) entryID(t *testing.T, provider, id string) string {
	t.Helper()
	pe, err := f.st.GetProviderEntry(context.Background(), db.GetProviderEntryParams{Provider: provider, ExternalID: id})
	if err != nil {
		t.Fatal(err)
	}
	return pe.EntryID.UUID.String()
}

func titles(sums []*metav1.EntrySummary) []string {
	out := make([]string, len(sums))
	for i, s := range sums {
		out[i] = s.GetTitle()
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBrowse(t *testing.T) {
	t.Parallel()
	f := setup(t)
	ctx := context.Background()
	browse := func(r *metav1.BrowseRequest) *metav1.BrowseResponse {
		t.Helper()
		res, err := f.meta.Browse(ctx, connect.NewRequest(r))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg
	}

	all := browse(&metav1.BrowseRequest{})
	if got := titles(all.GetEntries()); !eq(got, []string{"Attack of the Giants", "Quiet Orbit", "Lantern Keeper"}) {
		t.Errorf("default (popularity): %v", got)
	}
	if all.GetTotalSize() != 3 || all.GetNoProviders() {
		t.Errorf("total %d, no providers %v", all.GetTotalSize(), all.GetNoProviders())
	}
	first := all.GetEntries()[0]
	if first.GetYear() != 2009 || first.GetCover().GetBlurhash() != "LEHV6n" || first.GetRating() != 85 ||
		len(first.GetGenres()) != 1 || first.GetGenres()[0] != "Action" || first.GetAttributions()[0].GetProviderName() != "Fake" {
		t.Errorf("summary: %v", first)
	}

	cases := []struct {
		name string
		req  *metav1.BrowseRequest
		want []string
	}{
		{"rating", &metav1.BrowseRequest{Sort: metav1.SortField_SORT_FIELD_RATING}, []string{"Lantern Keeper", "Attack of the Giants", "Quiet Orbit"}},
		{"year reversed", &metav1.BrowseRequest{Sort: metav1.SortField_SORT_FIELD_YEAR, Reverse: true}, []string{"Quiet Orbit", "Attack of the Giants", "Lantern Keeper"}},
		{"title", &metav1.BrowseRequest{Sort: metav1.SortField_SORT_FIELD_TITLE}, []string{"Attack of the Giants", "Lantern Keeper", "Quiet Orbit"}},
		{"type", &metav1.BrowseRequest{Filter: &metav1.EntryFilter{Types: []metadatav1.EntryType{metadatav1.EntryType_ENTRY_TYPE_MANHWA}}}, []string{"Lantern Keeper"}},
		{"genre", &metav1.BrowseRequest{Filter: &metav1.EntryFilter{Genres: []string{"action"}}}, []string{"Attack of the Giants", "Quiet Orbit"}},
		{"all tags", &metav1.BrowseRequest{Filter: &metav1.EntryFilter{Tags: []string{"Titans", "Military"}}}, []string{"Attack of the Giants"}},
		{"exclude tag", &metav1.BrowseRequest{Filter: &metav1.EntryFilter{ExcludeTags: []string{"Titans"}}}, []string{"Quiet Orbit"}},
		{"status", &metav1.BrowseRequest{Filter: &metav1.EntryFilter{Statuses: []metadatav1.EntryStatus{metadatav1.EntryStatus_ENTRY_STATUS_RELEASING, metadatav1.EntryStatus_ENTRY_STATUS_HIATUS}}}, []string{"Quiet Orbit", "Lantern Keeper"}},
		{"content rating", &metav1.BrowseRequest{Filter: &metav1.EntryFilter{ContentRatings: []metadatav1.ContentRating{metadatav1.ContentRating_CONTENT_RATING_SAFE, metadatav1.ContentRating_CONTENT_RATING_SUGGESTIVE}}}, []string{"Attack of the Giants", "Lantern Keeper"}},
		{"years", &metav1.BrowseRequest{Filter: &metav1.EntryFilter{YearFrom: 2005, YearTo: 2010}}, []string{"Attack of the Giants"}},
		{"language", &metav1.BrowseRequest{Filter: &metav1.EntryFilter{Languages: []string{"ko"}}}, []string{"Lantern Keeper"}},
		{"title language", &metav1.BrowseRequest{Filter: &metav1.EntryFilter{TitleLanguages: []string{"ja"}}}, []string{"Attack of the Giants"}},
	}
	for _, c := range cases {
		if got := titles(browse(c.req).GetEntries()); !eq(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	// Paging.
	page1 := browse(&metav1.BrowseRequest{PageSize: 2})
	if len(page1.GetEntries()) != 2 || page1.GetNextPageToken() == "" {
		t.Fatalf("page 1: %v", page1)
	}
	page2 := browse(&metav1.BrowseRequest{PageSize: 2, PageToken: page1.GetNextPageToken()})
	if got := titles(page2.GetEntries()); !eq(got, []string{"Lantern Keeper"}) || page2.GetNextPageToken() != "" {
		t.Fatalf("page 2: %v", page2)
	}
	if _, err := f.meta.Browse(ctx, connect.NewRequest(&metav1.BrowseRequest{PageToken: "garbage"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad token: %v", err)
	}
	// Display titles in another language.
	ja := browse(&metav1.BrowseRequest{Language: "ja"})
	if ja.GetEntries()[0].GetTitle() != "進撃の巨人" || ja.GetEntries()[1].GetTitle() != "Quiet Orbit" {
		t.Errorf("ja titles: %v", titles(ja.GetEntries()))
	}
}

func TestSearch(t *testing.T) {
	t.Parallel()
	f := setup(t)
	ctx := context.Background()
	search := func(q string, filter *metav1.EntryFilter) *metav1.SearchResponse {
		t.Helper()
		res, err := f.meta.Search(ctx, connect.NewRequest(&metav1.SearchRequest{Query: q, Filter: filter}))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg
	}
	cases := []struct{ q, want, field string }{
		{"Attack of the Giants", "Attack of the Giants", "title"},
		{"shingeki", "Attack of the Giants", "title"}, // romanized title
		{"進撃", "Attack of the Giants", "title"},       // substring of a Japanese title
		{"lantenr keepr", "Lantern Keeper", "title"},  // typos
		{"hajime fictional", "Attack of the Giants", "person"},
		{"QUIET", "Quiet Orbit", "title"},
	}
	for _, c := range cases {
		res := search(c.q, nil)
		if len(res.GetHits()) == 0 || res.GetHits()[0].GetEntry().GetTitle() != c.want || res.GetHits()[0].GetMatchedField() != c.field {
			t.Errorf("%q: %v", c.q, res.GetHits())
		}
	}
	if res := search("zzzz nothing", nil); len(res.GetHits()) != 0 {
		t.Errorf("no match expected: %v", res.GetHits())
	}
	// Filters apply to search too.
	if res := search("giants", &metav1.EntryFilter{Types: []metadatav1.EntryType{metadatav1.EntryType_ENTRY_TYPE_MANHWA}}); len(res.GetHits()) != 0 {
		t.Errorf("filtered search: %v", res.GetHits())
	}
	// Merged entries don't show up.
	if res := search("Old Copy", nil); len(res.GetHits()) != 0 {
		t.Errorf("merged entry found: %v", res.GetHits())
	}
	if _, err := f.meta.Search(ctx, connect.NewRequest(&metav1.SearchRequest{Query: "  "})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("empty query: %v", err)
	}
}

func TestGetEntryAndRedirect(t *testing.T) {
	t.Parallel()
	f := setup(t)
	ctx := context.Background()
	id := f.entryID(t, "fake", "1")
	res, err := f.meta.GetEntry(ctx, connect.NewRequest(&metav1.GetEntryRequest{Id: id}))
	if err != nil {
		t.Fatal(err)
	}
	e := res.Msg.GetEntry()
	if e.GetTitle() != "Attack of the Giants" || len(e.GetTitles()) != 3 || e.GetType() != metadatav1.EntryType_ENTRY_TYPE_MANGA ||
		len(e.GetTags()) != 3 || e.GetCredits()[0].GetPerson().GetName() != "Hajime Fictional" ||
		e.GetDescriptions()[0].GetText() != "Walls and giants." || e.GetDates().GetStart().GetYear() != 2009 {
		t.Fatalf("entry: %v", e)
	}
	if len(e.GetSources()) != 1 || e.GetSources()[0].GetAttribution().GetProviderName() != "Fake" || e.GetFieldSources()["titles"] != "fake" {
		t.Errorf("provenance: %v %v", e.GetSources(), e.GetFieldSources())
	}
	if len(e.GetStats()) != 1 || e.GetStats()[0].GetStats().GetRating() != 85 {
		t.Errorf("stats: %v", e.GetStats())
	}
	rel := e.GetRelations()[0]
	if rel.GetEntryId() != f.entryID(t, "fake", "2") || rel.GetTitle() != "Lantern Keeper" {
		t.Errorf("relation: %v", rel)
	}
	found := false
	for _, x := range e.GetExternalIds() {
		found = found || (x.GetProvider() == "anilist" && x.GetId() == "53390")
	}
	if !found {
		t.Errorf("external ids: %v", e.GetExternalIds())
	}

	// The retired entry of record 4 redirects to the entry of record 3.
	var retired string
	if err := f.st.Pool.QueryRow(ctx, `SELECT from_id::text FROM entry_redirects`).Scan(&retired); err != nil {
		t.Fatal(err)
	}
	res, err = f.meta.GetEntry(ctx, connect.NewRequest(&metav1.GetEntryRequest{Id: retired}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetEntry().GetId() != f.entryID(t, "fake", "3") || res.Msg.GetRedirectedFrom() != retired {
		t.Fatalf("redirect: %v", res.Msg)
	}
	if _, err := f.meta.GetEntry(ctx, connect.NewRequest(&metav1.GetEntryRequest{Id: uuid.NewString()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("missing: %v", err)
	}
	if _, err := f.meta.GetEntry(ctx, connect.NewRequest(&metav1.GetEntryRequest{Id: "x"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad id: %v", err)
	}
}

func TestLookupExternal(t *testing.T) {
	t.Parallel()
	f := setup(t)
	ctx := context.Background()
	look := func(provider, id string) (*metav1.Entry, error) {
		res, err := f.meta.LookupExternal(ctx, connect.NewRequest(&metav1.LookupExternalRequest{Provider: provider, ExternalId: id}))
		if err != nil {
			return nil, err
		}
		return res.Msg.GetEntry(), nil
	}
	e, err := look("anilist", "53390")
	if err != nil || e.GetId() != f.entryID(t, "fake", "1") {
		t.Fatalf("anilist: %v %v", e, err)
	}
	// The merged record's old ID still finds the surviving entry.
	if e, err := look("fake", "4"); err != nil || e.GetId() != f.entryID(t, "fake", "3") {
		t.Fatalf("merged id: %v %v", e, err)
	}
	// Unknown to meta, known to the plugin: fetched and stored.
	f.fake.Lock()
	f.fake.Entries = append(f.fake.Entries, entry(f.fake, "9", func(e *metadatav1.ProviderEntry) {
		e.Titles = []*metadatav1.Title{{Text: "Late Arrival", Language: "en", IsPrimary: true}}
		e.ExternalIds = []*metadatav1.ExternalId{{Provider: "mal", Id: "777"}}
	}))
	f.fake.Unlock()
	if e, err := look("fake", "9"); err != nil || e.GetTitle() != "Late Arrival" {
		t.Fatalf("lookup through plugin: %v %v", e, err)
	}
	if _, err := look("fake", "404"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, err := look("", "1"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no provider: %v", err)
	}
}

func TestListTags(t *testing.T) {
	t.Parallel()
	f := setup(t)
	res, err := f.meta.ListTags(context.Background(), connect.NewRequest(&metav1.ListTagsRequest{GenresOnly: true}))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, tg := range res.Msg.GetTags() {
		got[tg.GetName()] = tg.GetEntryCount()
	}
	if len(got) != 2 || got["Action"] != 2 || got["Fantasy"] != 1 {
		t.Fatalf("genres: %v", got)
	}
	res, err = f.meta.ListTags(context.Background(), connect.NewRequest(&metav1.ListTagsRequest{Prefix: "tit"}))
	if err != nil || len(res.Msg.GetTags()) != 1 || res.Msg.GetTags()[0].GetName() != "Titans" {
		t.Fatalf("prefix: %v %v", res, err)
	}
}

func TestRESTAndAuth(t *testing.T) {
	t.Parallel()
	f := setup(t, "k1")
	get := func(path, key string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), "GET", f.srv.URL+path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(res.Body).Decode(&body)
		return res.StatusCode, body
	}
	if code, _ := get("/v1/entries", ""); code != http.StatusUnauthorized {
		t.Fatalf("no key: %d", code)
	}
	if code, _ := get("/v1/entries", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", code)
	}
	code, body := get("/v1/entries?filter.types=ENTRY_TYPE_MANHWA&sort=SORT_FIELD_RATING", "k1")
	entries, _ := body["entries"].([]any)
	if code != http.StatusOK || len(entries) != 1 {
		t.Fatalf("browse over REST: %d %v", code, body)
	}
	code, body = get("/v1/search?query="+url.QueryEscape("進撃"), "k1")
	if hits, _ := body["hits"].([]any); code != http.StatusOK || len(hits) != 1 {
		t.Fatalf("search over REST: %d %v", code, body)
	}
	code, body = get("/v1/lookup/anilist/53390", "k1")
	if e, _ := body["entry"].(map[string]any); code != http.StatusOK || e["title"] != "Attack of the Giants" {
		t.Fatalf("lookup over REST: %d %v", code, body)
	}
	if code, _ := get("/v1/entries/"+uuid.NewString(), "k1"); code != http.StatusNotFound {
		t.Fatalf("missing entry over REST: %d", code)
	}
	if code, _ := get("/healthz", ""); code != http.StatusOK {
		t.Fatalf("healthz: %d", code)
	}
}
