// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// Typesense talks to a Typesense server over its REST API. Every index is
// a collection; the live name is a collection alias, so a rebuild swaps
// atomically.
type Typesense struct {
	c *restClient
}

// NewTypesense returns the Typesense engine at baseURL (e.g.
// http://typesense:8108) with an API key that may manage collections and
// aliases.
func NewTypesense(baseURL, apiKey string) *Typesense {
	return &Typesense{c: newRESTClient(baseURL, "X-TYPESENSE-API-KEY", apiKey)}
}

func (ts *Typesense) Name() string { return "typesense" }

func (ts *Typesense) Check(ctx context.Context) error {
	var h struct {
		OK bool `json:"ok"`
	}
	if err := ts.c.do(ctx, http.MethodGet, "/health", nil, "", &h); err != nil {
		return err
	}
	if !h.OK {
		return errors.New("typesense: not healthy")
	}
	// The health endpoint needs no key; check the key too.
	return ts.c.do(ctx, http.MethodGet, "/collections?limit=1", nil, "", nil)
}

type tsField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Facet    bool   `json:"facet,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Sort     bool   `json:"sort,omitempty"`
	Infix    bool   `json:"infix,omitempty"`
	Locale   string `json:"locale,omitempty"`
	Index    *bool  `json:"index,omitempty"`
}

// tsSchema is the collection schema of Document. Substring (infix)
// matching costs memory, so only the CJK title fields have it.
func tsSchema(name string) map[string]any {
	f := func(name, typ string) tsField { return tsField{Name: name, Type: typ} }
	fields := []tsField{
		f("title", "string"),
		{Name: "title_sort", Type: "string", Sort: true},
		{Name: "titles_ja", Type: "string[]", Locale: "ja", Infix: true},
		{Name: "titles_zh", Type: "string[]", Locale: "zh", Infix: true},
		{Name: "titles_ko", Type: "string[]", Locale: "ko", Infix: true},
		f("titles_latn", "string[]"),
		f("titles_en", "string[]"),
		f("titles_other", "string[]"),
		f("people", "string[]"),
		{Name: "genre_keys", Type: "string[]", Facet: true},
		f("genre_names", "string[]"),
		f("tag_keys", "string[]"),
		f("tag_names", "string[]"),
		{Name: "facet_tags", Type: "string[]", Facet: true},
		{Name: "type", Type: "string", Facet: true},
		{Name: "status", Type: "string", Facet: true},
		{Name: "content_rating", Type: "string", Facet: true},
		{Name: "original_language", Type: "string", Facet: true},
		f("title_languages", "string[]"),
		{Name: "year", Type: "int32", Optional: true, Sort: true},
		{Name: "rating", Type: "float", Optional: true, Sort: true},
		{Name: "popularity", Type: "float", Optional: true, Sort: true},
		{Name: "popularity_rank", Type: "int64", Optional: true, Sort: true},
		{Name: "updated_at", Type: "int64", Sort: true},
	}
	return map[string]any{"name": name, "fields": fields}
}

func (ts *Typesense) CreateIndex(ctx context.Context, name string) error {
	return ts.c.do(ctx, http.MethodPost, "/collections", tsSchema(name), "", nil)
}

func (ts *Typesense) DeleteIndex(ctx context.Context, name string) error {
	err := ts.c.do(ctx, http.MethodDelete, "/collections/"+url.PathEscape(name), nil, "", nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func isNotFound(err error) bool {
	var ee *EngineError
	return errors.As(err, &ee) && ee.Status == http.StatusNotFound
}

func (ts *Typesense) HasIndex(ctx context.Context, name string) (bool, error) {
	err := ts.c.do(ctx, http.MethodGet, "/collections/"+url.PathEscape(name), nil, "", nil)
	if isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func (ts *Typesense) Upsert(ctx context.Context, index string, docs []Document) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return err
		}
	}
	var out []byte
	if err := ts.c.do(ctx, http.MethodPost, "/collections/"+url.PathEscape(index)+"/documents/import?action=upsert",
		body.Bytes(), "text/plain", &out); err != nil {
		return err
	}
	// One JSON line per document; any failure fails the batch.
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var line struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return fmt.Errorf("typesense import: %w", err)
		}
		if !line.Success {
			return fmt.Errorf("typesense import: %s", line.Error)
		}
	}
	return sc.Err()
}

func (ts *Typesense) Delete(ctx context.Context, index string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	filter := "id:[" + strings.Join(quoteAll(ids), ",") + "]"
	return ts.c.do(ctx, http.MethodDelete, "/collections/"+url.PathEscape(index)+"/documents?filter_by="+url.QueryEscape(filter), nil, "", nil)
}

func (ts *Typesense) Count(ctx context.Context, index string) (int64, error) {
	var c struct {
		NumDocuments int64 `json:"num_documents"`
	}
	err := ts.c.do(ctx, http.MethodGet, "/collections/"+url.PathEscape(index), nil, "", &c)
	return c.NumDocuments, err
}

func (ts *Typesense) Activate(ctx context.Context, live, index string) (string, string, error) {
	var old struct {
		CollectionName string `json:"collection_name"`
	}
	if err := ts.c.do(ctx, http.MethodGet, "/aliases/"+url.PathEscape(live), nil, "", &old); err != nil && !isNotFound(err) {
		return "", "", err
	}
	if err := ts.c.do(ctx, http.MethodPut, "/aliases/"+url.PathEscape(live), map[string]string{"collection_name": index}, "", nil); err != nil {
		return "", "", err
	}
	return index, old.CollectionName, nil
}

// quote wraps a filter value in backticks, so keys with "/" and spaces
// are taken literally.
func quote(v string) string { return "`" + strings.ReplaceAll(v, "`", "") + "`" }

func quoteAll(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = quote(v)
	}
	return out
}

// tsFilter maps a Filter onto filter_by.
func tsFilter(f Filter) string {
	var parts []string
	in := func(field string, vals []string) {
		if len(vals) > 0 {
			parts = append(parts, field+":=["+strings.Join(quoteAll(vals), ",")+"]")
		}
	}
	in("type", lower(f.Types))
	in("status", lower(f.Statuses))
	in("content_rating", lower(f.ContentRatings))
	in("original_language", lower(f.Languages))
	in("title_languages", lower(f.TitleLanguages))
	if f.YearFrom > 0 {
		parts = append(parts, "year:>="+strconv.Itoa(f.YearFrom))
	}
	if f.YearTo > 0 {
		parts = append(parts, "year:<="+strconv.Itoa(f.YearTo))
	}
	anyOf := func(keysField, namesField string, keys, names []string) string {
		var or []string
		if len(keys) > 0 {
			or = append(or, keysField+":=["+strings.Join(quoteAll(keys), ",")+"]")
		}
		if len(names) > 0 {
			or = append(or, namesField+":=["+strings.Join(quoteAll(names), ",")+"]")
		}
		return "(" + strings.Join(or, " || ") + ")"
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
		if len(keys) > 0 {
			parts = append(parts, "tag_keys:!=["+strings.Join(quoteAll(keys), ",")+"]")
		}
		if len(names) > 0 {
			parts = append(parts, "tag_names:!=["+strings.Join(quoteAll(names), ",")+"]")
		}
	}
	return strings.Join(parts, " && ")
}

// tsSort maps a sort order onto sort_by (at most three fields). Missing
// values sort last both ways, as on PostgreSQL.
func tsSort(s Sort, reverse, text bool) string {
	dir := func(natural string) string {
		if reverse == (natural == "asc") {
			return "desc"
		}
		return "asc"
	}
	last := func(field, natural string) string { return field + "(missing_values: last):" + dir(natural) }
	if s == SortDefault {
		s = SortPopularity
		if text {
			s = SortRelevance
		}
	}
	switch s {
	case SortRating:
		return last("rating", "desc") + ",popularity_rank(missing_values: last):asc"
	case SortUpdated:
		return "updated_at:" + dir("desc")
	case SortYear:
		return last("year", "desc") + ",popularity_rank(missing_values: last):asc"
	case SortTitle:
		return "title_sort:" + dir("asc")
	case SortRelevance:
		if !text {
			return last("popularity_rank", "asc") + "," + last("popularity", "desc")
		}
		return "_text_match:" + dir("desc") + ",popularity_rank(missing_values: last):asc"
	default:
		return last("popularity_rank", "asc") + "," + last("popularity", "desc")
	}
}

type tsField2 struct {
	field  string
	weight int
	infix  string
}

var tsQueryBy = []tsField2{
	{"title", 10, "off"},
	{"titles_en", 8, "off"},
	{"titles_latn", 8, "off"},
	{"titles_ja", 8, "fallback"},
	{"titles_zh", 8, "fallback"},
	{"titles_ko", 8, "fallback"},
	{"titles_other", 7, "off"},
	{"people", 4, "off"},
}

// tsOrders returns the query_by orders to search with. Typesense splits
// the query into words with the locale of the first field, so a Japanese,
// Chinese or Korean query must start with that field. A query of Han
// characters only may be either Japanese or Chinese: both orders run and
// their hits are merged.
func tsOrders(text string) [][]tsField2 {
	// Typesense orders fields by weight, so the leading field also gets
	// the highest one.
	first := func(field string) []tsField2 {
		var head, rest []tsField2
		for _, f := range tsQueryBy {
			if f.field == field {
				f.weight = 11
				head = append(head, f)
			} else {
				rest = append(rest, f)
			}
		}
		return append(head, rest...)
	}
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
		return [][]tsField2{first("titles_ja")}
	case hangul:
		return [][]tsField2{first("titles_ko")}
	case han:
		return [][]tsField2{first("titles_ja"), first("titles_zh")}
	}
	return [][]tsField2{tsQueryBy}
}

// tsFacetFields are the collection fields counted for each facet.
var tsFacetFields = map[string]string{
	"genre_keys": FacetGenres, "facet_tags": FacetTags, "type": FacetType, "status": FacetStatus,
	"content_rating": FacetContentRating, "original_language": FacetOriginalLanguage,
}

// tsMaxPerPage is Typesense's page size limit.
const tsMaxPerPage = 250

type tsResult struct {
	Found int64 `json:"found"`
	Hits  []struct {
		Document   map[string]any `json:"document"`
		Highlights []struct {
			Field   string `json:"field"`
			Indices []int  `json:"indices"`
		} `json:"highlights"`
		TextMatch float64 `json:"text_match"`
	} `json:"hits"`
	FacetCounts []struct {
		FieldName string `json:"field_name"`
		Counts    []struct {
			Value string `json:"value"`
			Count int64  `json:"count"`
		} `json:"counts"`
	} `json:"facet_counts"`
	Code  int    `json:"code"`
	Error string `json:"error"`
}

func (ts *Typesense) Search(ctx context.Context, live string, q Query) (Result, error) {
	if q.Limit <= 0 {
		q.Limit = 24
	}
	text := strings.TrimSpace(q.Text)
	orders := tsOrders(text)
	searches := make([]map[string]any, 0, len(orders))
	for _, order := range orders {
		var by, weights, infix []string
		for _, f := range order {
			by = append(by, f.field)
			weights = append(weights, strconv.Itoa(f.weight))
			infix = append(infix, f.infix)
		}
		s := map[string]any{
			"collection":            live,
			"q":                     "*",
			"query_by":              strings.Join(by, ","),
			"query_by_weights":      strings.Join(weights, ","),
			"infix":                 strings.Join(infix, ","),
			"highlight_fields":      strings.Join(by, ","),
			"include_fields":        strings.Join(append(by, "id"), ","),
			"num_typos":             2,
			"drop_tokens_threshold": 0,
			"sort_by":               tsSort(q.Sort, q.Reverse, text != ""),
		}
		if text != "" {
			s["q"] = text
		}
		if f := tsFilter(q.Filter); f != "" {
			s["filter_by"] = f
		}
		if q.Facets {
			s["facet_by"] = "genre_keys,facet_tags,type,status,content_rating,original_language"
			s["max_facet_values"] = 50
		}
		searches = append(searches, s)
	}
	var res tsResult
	if len(searches) == 1 {
		searches[0]["limit"], searches[0]["offset"] = q.Limit, q.Offset
		var multi struct {
			Results []tsResult `json:"results"`
		}
		if err := ts.c.do(ctx, http.MethodPost, "/multi_search", map[string]any{"searches": searches}, "", &multi); err != nil {
			return Result{}, err
		}
		if len(multi.Results) != 1 {
			return Result{}, errors.New("typesense: no search result")
		}
		res = multi.Results[0]
	} else {
		// A union pages over the merged hits: fetch up to the end of the
		// page and cut.
		per := min(q.Offset+q.Limit, tsMaxPerPage)
		path := "/multi_search?page=1&per_page=" + strconv.Itoa(per)
		if err := ts.c.do(ctx, http.MethodPost, path, map[string]any{"union": true, "searches": searches}, "", &res); err != nil {
			return Result{}, err
		}
		if q.Offset >= len(res.Hits) {
			res.Hits = nil
		} else {
			res.Hits = res.Hits[q.Offset:]
		}
	}
	if res.Error != "" {
		return Result{}, &EngineError{Status: res.Code, Message: res.Error}
	}
	out := Result{Total: res.Found}
	for _, h := range res.Hits {
		id, err := uuid.Parse(fmt.Sprint(h.Document["id"]))
		if err != nil {
			return Result{}, fmt.Errorf("typesense: document id %v: %w", h.Document["id"], err)
		}
		hit := Hit{ID: id, Score: h.TextMatch}
		if text != "" && len(h.Highlights) > 0 {
			hl := h.Highlights[0]
			hit.Field = "title"
			if hl.Field == "people" {
				hit.Field = "person"
			}
			hit.Matched = pick(h.Document[hl.Field], hl.Indices)
		}
		out.Hits = append(out.Hits, hit)
	}
	for _, fc := range res.FacetCounts {
		facet := Facet{Field: tsFacetFields[fc.FieldName]}
		for _, c := range fc.Counts {
			facet.Values = append(facet.Values, FacetValue{Value: c.Value, Count: c.Count})
		}
		out.Facets = append(out.Facets, facet)
	}
	sortFacets(out.Facets)
	return out, nil
}

// pick returns the matched value of a highlighted field: the string, or
// the first matched element of an array.
func pick(v any, indices []int) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		i := 0
		if len(indices) > 0 {
			i = indices[0]
		}
		if i < len(x) {
			return fmt.Sprint(x[i])
		}
	}
	return ""
}
