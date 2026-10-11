// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// Meilisearch talks to a Meilisearch server (the MIT edition is enough)
// over its REST API. The live index keeps its name; a rebuild fills a new
// index and swaps the two atomically with swap-indexes.
type Meilisearch struct {
	c *restClient
}

// NewMeilisearch returns the Meilisearch engine at baseURL (e.g.
// http://meilisearch:7700) with a key that may manage indexes.
func NewMeilisearch(baseURL, apiKey string) *Meilisearch {
	c := newRESTClient(baseURL, "Authorization", "")
	if apiKey != "" {
		c.key = "Bearer " + apiKey
	}
	return &Meilisearch{c: c}
}

func (ms *Meilisearch) Name() string { return "meilisearch" }

// meiliMaxTotalHits caps how many matches a query counts and pages
// through, like PostgreSQL's candidate cap for searches. Browsing stays on
// PostgreSQL by default, which counts everything.
const meiliMaxTotalHits = 20000

func (ms *Meilisearch) Check(ctx context.Context) error {
	var h struct {
		Status string `json:"status"`
	}
	if err := ms.c.do(ctx, http.MethodGet, "/health", nil, "", &h); err != nil {
		return err
	}
	if h.Status != "available" {
		return fmt.Errorf("meilisearch: %s", h.Status)
	}
	// The health endpoint needs no key; check the key too.
	return ms.c.do(ctx, http.MethodGet, "/indexes?limit=1", nil, "", nil)
}

type meiliTask struct {
	TaskUID int64 `json:"taskUid"`
}

// wait follows an asynchronous task until it ends.
func (ms *Meilisearch) wait(ctx context.Context, t meiliTask) error {
	delay := 10 * time.Millisecond
	for {
		var st struct {
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := ms.c.do(ctx, http.MethodGet, "/tasks/"+strconv.FormatInt(t.TaskUID, 10), nil, "", &st); err != nil {
			return err
		}
		switch st.Status {
		case "succeeded":
			return nil
		case "failed", "canceled":
			if st.Error != nil {
				return &EngineError{Status: http.StatusUnprocessableEntity, Message: st.Error.Code + ": " + st.Error.Message}
			}
			return fmt.Errorf("meilisearch: task %d %s", t.TaskUID, st.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, 500*time.Millisecond)
	}
}

// run sends a request that starts a task and waits for it.
func (ms *Meilisearch) run(ctx context.Context, method, path string, body any) error {
	var t meiliTask
	if err := ms.c.do(ctx, method, path, body, "", &t); err != nil {
		return err
	}
	return ms.wait(ctx, t)
}

var meiliSearchable = []string{"title", "titles_en", "titles_latn", "titles_ja", "titles_zh", "titles_ko", "titles_other", "people"}

func meiliSettings() map[string]any {
	return map[string]any{
		"searchableAttributes": meiliSearchable,
		"filterableAttributes": []string{
			"type", "status", "content_rating", "original_language", "title_languages", "year",
			"genre_keys", "genre_names", "tag_keys", "tag_names", "facet_tags",
		},
		"sortableAttributes": []string{"title_sort", "year", "rating", "popularity", "popularity_rank", "updated_at"},
		// Text first; the requested sort breaks ties. Without text the
		// sort decides alone.
		"rankingRules": []string{"words", "typo", "proximity", "attribute", "sort", "exactness"},
		"localizedAttributes": []map[string]any{
			{"attributePatterns": []string{"titles_ja"}, "locales": []string{"jpn"}},
			{"attributePatterns": []string{"titles_zh"}, "locales": []string{"cmn"}},
			{"attributePatterns": []string{"titles_ko"}, "locales": []string{"kor"}},
		},
		"pagination": map[string]any{"maxTotalHits": meiliMaxTotalHits},
		"faceting":   map[string]any{"maxValuesPerFacet": 50},
	}
}

func (ms *Meilisearch) CreateIndex(ctx context.Context, name string) error {
	if err := ms.run(ctx, http.MethodPost, "/indexes", map[string]string{"uid": name, "primaryKey": "id"}); err != nil {
		return err
	}
	return ms.run(ctx, http.MethodPatch, "/indexes/"+url.PathEscape(name)+"/settings", meiliSettings())
}

func (ms *Meilisearch) DeleteIndex(ctx context.Context, name string) error {
	err := ms.run(ctx, http.MethodDelete, "/indexes/"+url.PathEscape(name), nil)
	if isNotFound(err) || isMeiliCode(err, "index_not_found") {
		return nil
	}
	return err
}

func isMeiliCode(err error, code string) bool {
	var ee *EngineError
	return errors.As(err, &ee) && strings.HasPrefix(ee.Message, code+":")
}

func (ms *Meilisearch) HasIndex(ctx context.Context, name string) (bool, error) {
	err := ms.c.do(ctx, http.MethodGet, "/indexes/"+url.PathEscape(name), nil, "", nil)
	if isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func (ms *Meilisearch) Upsert(ctx context.Context, index string, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	// POST replaces whole documents, so cleared fields don't linger.
	return ms.run(ctx, http.MethodPost, "/indexes/"+url.PathEscape(index)+"/documents?primaryKey=id", docs)
}

func (ms *Meilisearch) Delete(ctx context.Context, index string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return ms.run(ctx, http.MethodPost, "/indexes/"+url.PathEscape(index)+"/documents/delete-batch", ids)
}

func (ms *Meilisearch) Count(ctx context.Context, index string) (int64, error) {
	var st struct {
		NumberOfDocuments int64 `json:"numberOfDocuments"`
	}
	err := ms.c.do(ctx, http.MethodGet, "/indexes/"+url.PathEscape(index)+"/stats", nil, "", &st)
	return st.NumberOfDocuments, err
}

// Activate swaps the built index with the live one: the live name then
// holds the new documents, and the built index's name the old ones, which
// is dropped.
func (ms *Meilisearch) Activate(ctx context.Context, live, index string) (string, string, error) {
	ok, err := ms.HasIndex(ctx, live)
	if err != nil {
		return "", "", err
	}
	if !ok {
		if err := ms.CreateIndex(ctx, live); err != nil {
			return "", "", err
		}
	}
	if err := ms.run(ctx, http.MethodPost, "/swap-indexes", []map[string]any{{"indexes": []string{live, index}}}); err != nil {
		return "", "", err
	}
	return live, index, nil
}

// meiliQuote quotes a filter value.
func meiliQuote(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

func meiliIn(field string, vals []string) string {
	q := make([]string, len(vals))
	for i, v := range vals {
		q[i] = meiliQuote(v)
	}
	return field + " IN [" + strings.Join(q, ", ") + "]"
}

// meiliFilter maps a Filter onto Meilisearch's filter expression.
func meiliFilter(f Filter) string {
	var parts []string
	in := func(field string, vals []string) {
		if len(vals) > 0 {
			parts = append(parts, meiliIn(field, vals))
		}
	}
	in("type", lower(f.Types))
	in("status", lower(f.Statuses))
	in("content_rating", lower(f.ContentRatings))
	in("original_language", lower(f.Languages))
	in("title_languages", lower(f.TitleLanguages))
	if f.YearFrom > 0 {
		parts = append(parts, "year >= "+strconv.Itoa(f.YearFrom))
	}
	if f.YearTo > 0 {
		parts = append(parts, "year <= "+strconv.Itoa(f.YearTo))
	}
	anyOf := func(keysField, namesField string, keys, names []string) string {
		var or []string
		if len(keys) > 0 {
			or = append(or, meiliIn(keysField, keys))
		}
		if len(names) > 0 {
			or = append(or, meiliIn(namesField, names))
		}
		return "(" + strings.Join(or, " OR ") + ")"
	}
	if keys, names := tagRefs(f.Genres); len(keys)+len(names) > 0 {
		parts = append(parts, anyOf("genre_keys", "genre_names", keys, names))
	}
	for _, ref := range f.Tags {
		if keys, names := tagRefs([]string{ref}); len(keys)+len(names) > 0 {
			parts = append(parts, anyOf("tag_keys", "tag_names", keys, names))
		}
	}
	if keys, names := tagRefs(f.ExcludeTags); len(keys)+len(names) > 0 {
		parts = append(parts, "NOT "+anyOf("tag_keys", "tag_names", keys, names))
	}
	return strings.Join(parts, " AND ")
}

// meiliSort maps a sort order. Documents without the field come last
// either way, as on PostgreSQL. Relevance needs no sort: the text ranking
// rules come first and popularity breaks ties.
func meiliSort(s Sort, reverse, text bool) []string {
	dir := func(natural string) string {
		if reverse == (natural == "asc") {
			return "desc"
		}
		return "asc"
	}
	if s == SortDefault {
		s = SortPopularity
		if text {
			s = SortRelevance
		}
	}
	switch s {
	case SortRating:
		return []string{"rating:" + dir("desc"), "popularity_rank:asc"}
	case SortUpdated:
		return []string{"updated_at:" + dir("desc")}
	case SortYear:
		return []string{"year:" + dir("desc"), "popularity_rank:asc"}
	case SortTitle:
		return []string{"title_sort:" + dir("asc")}
	case SortRelevance:
		if text {
			return []string{"popularity_rank:asc"}
		}
	}
	return []string{"popularity_rank:" + dir("asc"), "popularity:" + dir("desc")}
}

// meiliLocales picks the query languages by script, so Japanese, Chinese
// and Korean queries are split into words the way the titles were.
func meiliLocales(text string) []string {
	var kana, hangul, han bool
	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Hiragana, unicode.Katakana):
			kana = true
		case unicode.Is(unicode.Hangul, r):
			hangul = true
		case unicode.Is(unicode.Han, r):
			han = true
		}
	}
	switch {
	case kana:
		return []string{"jpn"}
	case hangul:
		return []string{"kor"}
	case han:
		// Han only may be Japanese or Chinese. Japanese segmentation
		// finds both in practice; asking for both languages makes
		// Meilisearch split Japanese words the Chinese way.
		return []string{"jpn"}
	}
	return nil
}

var meiliFacetFields = map[string]string{
	"genre_keys": FacetGenres, "facet_tags": FacetTags, "type": FacetType, "status": FacetStatus,
	"content_rating": FacetContentRating, "original_language": FacetOriginalLanguage,
}

func (ms *Meilisearch) Search(ctx context.Context, live string, q Query) (Result, error) {
	if q.Limit <= 0 {
		q.Limit = 24
	}
	text := strings.TrimSpace(q.Text)
	body := map[string]any{
		"q":                    text,
		"sort":                 meiliSort(q.Sort, q.Reverse, text != ""),
		"attributesToRetrieve": append([]string{"id"}, meiliSearchable...),
		"showMatchesPosition":  text != "",
		// Every word must match (with typos), as on the other backends;
		// Meilisearch would otherwise drop words until something matches.
		"matchingStrategy": "all",
	}
	// Pages give an exact total; a page that doesn't start on a page
	// boundary is cut from a longer first page.
	cut := 0
	if q.Offset%q.Limit == 0 {
		body["page"], body["hitsPerPage"] = q.Offset/q.Limit+1, q.Limit
	} else {
		body["page"], body["hitsPerPage"] = 1, q.Offset+q.Limit
		cut = q.Offset
	}
	if f := meiliFilter(q.Filter); f != "" {
		body["filter"] = f
	}
	if l := meiliLocales(text); l != nil {
		body["locales"] = l
	}
	if q.Facets {
		body["facets"] = []string{"genre_keys", "facet_tags", "type", "status", "content_rating", "original_language"}
	}
	var res struct {
		Hits              []map[string]any            `json:"hits"`
		TotalHits         int64                       `json:"totalHits"`
		FacetDistribution map[string]map[string]int64 `json:"facetDistribution"`
	}
	if err := ms.c.do(ctx, http.MethodPost, "/indexes/"+url.PathEscape(live)+"/search", body, "", &res); err != nil {
		return Result{}, err
	}
	if cut > 0 {
		if cut >= len(res.Hits) {
			res.Hits = nil
		} else {
			res.Hits = res.Hits[cut:]
		}
	}
	out := Result{Total: res.TotalHits}
	for _, h := range res.Hits {
		id, err := uuid.Parse(fmt.Sprint(h["id"]))
		if err != nil {
			return Result{}, fmt.Errorf("meilisearch: document id %v: %w", h["id"], err)
		}
		hit := Hit{ID: id}
		if text != "" {
			hit.Field, hit.Matched = meiliMatched(h)
		}
		out.Hits = append(out.Hits, hit)
	}
	for _, field := range []string{"genre_keys", "facet_tags", "type", "status", "content_rating", "original_language"} {
		dist, ok := res.FacetDistribution[field]
		if !ok {
			continue
		}
		facet := Facet{Field: meiliFacetFields[field]}
		for v, n := range dist {
			facet.Values = append(facet.Values, FacetValue{Value: v, Count: n})
		}
		sortFacetValues(facet.Values)
		out.Facets = append(out.Facets, facet)
	}
	return out, nil
}

// meiliMatched finds the best matched attribute of a hit: titles before
// people, in searchable order.
func meiliMatched(h map[string]any) (field, matched string) {
	pos, _ := h["_matchesPosition"].(map[string]any)
	for _, attr := range meiliSearchable {
		for key, v := range pos {
			name, idx, _ := strings.Cut(key, ".")
			if name != attr {
				continue
			}
			field = "title"
			if attr == "people" {
				field = "person"
			}
			i := 0
			if idx != "" {
				i, _ = strconv.Atoi(idx)
			} else if ps, ok := v.([]any); ok && len(ps) > 0 {
				if p, ok := ps[0].(map[string]any); ok {
					if ind, ok := p["indices"].([]any); ok && len(ind) > 0 {
						f, _ := ind[0].(float64)
						i = int(f)
					}
				}
			}
			return field, pick(h[attr], []int{i})
		}
	}
	return "", ""
}
