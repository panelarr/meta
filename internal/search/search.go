// SPDX-License-Identifier: GPL-3.0-or-later

// Package search finds and lists entries. Callers use the Index interface;
// the built-in backend is PostgreSQL itself (full-text search and pg_trgm
// trigram matching), and external engines can plug in behind the same
// interface later. The database stays the source of truth: an index only
// returns entry IDs, and callers load the entries from the database.
package search

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// Index finds entries.
type Index interface {
	// Query runs a search (Text set) or a browse (Text empty).
	Query(ctx context.Context, q Query) (Result, error)
	// Upsert tells the index that entries changed. Backends that read
	// the database directly do nothing.
	Upsert(ctx context.Context, ids ...uuid.UUID) error
	// Delete tells the index that entries are gone.
	Delete(ctx context.Context, ids ...uuid.UUID) error
	// Rebuild re-indexes everything from the database.
	Rebuild(ctx context.Context) error
	// Capabilities says what the backend can do.
	Capabilities() Capabilities
}

// Capabilities describe a backend.
type Capabilities struct {
	Name string
	// TypoTolerance: near matches are found ("naruro" finds "Naruto").
	TypoTolerance bool
	// Substring: matches inside words, which matters for languages
	// written without spaces (Japanese, Chinese).
	Substring bool
	// Facets: counts per filter value.
	Facets bool
}

// Sort orders results.
type Sort int

const (
	SortDefault Sort = iota
	SortPopularity
	SortRating
	SortUpdated
	SortYear
	SortTitle
	SortRelevance
)

// Filter narrows results. Slices match any of their values; different
// fields must all match. Values use meta's short names ("manga",
// "releasing", "safe"); tag and genre names are matched by normalized name.
type Filter struct {
	Genres          []string
	Tags            []string // the entry must have all of them
	ExcludeTags     []string
	Types           []string
	Statuses        []string
	ContentRatings  []string
	Languages       []string
	YearFrom        int
	YearTo          int
	TitleLanguages  []string
	IncludeInactive bool
}

// Query is a search or browse request.
type Query struct {
	// Text to search for in titles and people. Empty: browse.
	Text    string
	Filter  Filter
	Sort    Sort
	Reverse bool
	Limit   int
	Offset  int
	// Facets asks for counts per filter value, from backends that can.
	Facets bool
}

// Hit is one result.
type Hit struct {
	ID    uuid.UUID
	Score float64
	// Matched is the title or name that matched, Field is "title" or
	// "person". Empty for browse.
	Matched string
	Field   string
}

// Result is a page of hits.
type Result struct {
	Hits  []Hit
	Total int64
	// Facets, when asked for and the backend has the Facets capability.
	Facets []Facet
	// Backend is the backend that answered: an engine's name, or
	// "postgres" when an engine fell back.
	Backend string
}

// Facet fields.
const (
	FacetGenres           = "genres"
	FacetTags             = "tags"
	FacetType             = "type"
	FacetStatus           = "status"
	FacetContentRating    = "content_rating"
	FacetOriginalLanguage = "original_language"
)

// FacetFields are the fields engines count, in display order.
var FacetFields = []string{FacetGenres, FacetTags, FacetType, FacetStatus, FacetContentRating, FacetOriginalLanguage}

// Facet is the counts of one field's values among all matches. Genre and
// tag values are tag keys ("genre/fantasy").
type Facet struct {
	Field  string
	Values []FacetValue
}

// sortFacets puts facets in FacetFields order.
func sortFacets(fs []Facet) {
	order := map[string]int{}
	for i, f := range FacetFields {
		order[f] = i
	}
	slices.SortStableFunc(fs, func(a, b Facet) int { return order[a.Field] - order[b.Field] })
}

// sortFacetValues orders values by count, then value.
func sortFacetValues(vs []FacetValue) {
	slices.SortFunc(vs, func(a, b FacetValue) int {
		if a.Count != b.Count {
			return int(b.Count - a.Count)
		}
		return strings.Compare(a.Value, b.Value)
	})
}

// FacetValue is one value and how many matches have it.
type FacetValue struct {
	Value string
	Count int64
}
