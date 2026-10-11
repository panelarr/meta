// SPDX-License-Identifier: GPL-3.0-or-later

// Package duplicates finds entries that are probably the same work but
// aren't linked, scores each pair, and keeps them for a curator to confirm
// or dismiss. The same scoring can later match series from elsewhere to
// entries.
package duplicates

import (
	"crypto/sha256"
	"math"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// ScoreVersion changes when scoring changes, so model verdicts of pairs
// compared the old way aren't reused.
const ScoreVersion = 1

// Ref is a provider's ID: "anilist:30013".
type Ref struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

func (r Ref) String() string { return r.Provider + ":" + r.ID }

// Features is what scoring compares of one entry.
type Features struct {
	ID       uuid.UUID
	State    string
	Title    string
	Type     string
	Language string
	Year     int
	// Normalized titles (textnorm.Key) in every language.
	Titles []string
	// Normalized names of the people credited.
	People []string
	// The entry's own provider records.
	Records []Ref
	// Every ID pointing at the entry: its records' and the IDs they report
	// for other sites.
	ExternalIDs []Ref
	// Records the entry's relations point at (sequels, adaptations...).
	Relations   []Ref
	ContentHash []byte
}

// Providers returns the providers of the entry's records.
func (f *Features) Providers() []string {
	var out []string
	for _, r := range f.Records {
		if !slices.Contains(out, r.Provider) {
			out = append(out, r.Provider)
		}
	}
	slices.Sort(out)
	return out
}

// Signals explain a score. They are stored with the pair and shown to the
// curator; the model judge gets the entries themselves.
type Signals struct {
	// Title: the best title similarity (1 = the same normalized title) and
	// the two titles.
	Title        float64  `json:"title"`
	TitleA       string   `json:"title_a"`
	TitleB       string   `json:"title_b"`
	SharedPeople []string `json:"shared_people,omitempty"`
	// People: share of credited people in common (0..1); -1 when one side
	// has none.
	People    float64 `json:"people"`
	YearA     int     `json:"year_a,omitempty"`
	YearB     int     `json:"year_b,omitempty"`
	TypeA     string  `json:"type_a,omitempty"`
	TypeB     string  `json:"type_b,omitempty"`
	LanguageA string  `json:"language_a,omitempty"`
	LanguageB string  `json:"language_b,omitempty"`
	// SharedIDs: IDs at another site both entries report.
	SharedIDs []string `json:"shared_ids,omitempty"`
	// SameProvider: both entries come from the same provider, which keeps
	// them apart on purpose unless its data has duplicates.
	SameProvider bool `json:"same_provider,omitempty"`
}

// Skip reasons: evidence that the two are different works.
const (
	SkipConflictingIDs = "conflicting_ids"
	SkipRelated        = "related"
	SkipSameProvider   = "same_provider"
	SkipInactive       = "inactive"
)

// Options tune scoring.
type Options struct {
	// SkipSameProvider never pairs entries whose records come from the
	// same providers only (the strict linking rule).
	SkipSameProvider bool
}

// Score compares two entries. It returns the score (0..1) and its signals,
// or a skip reason when the two are known to be different works.
func Score(a, b *Features, opts Options) (float64, Signals, string) {
	if a.State != "active" || b.State != "active" {
		return 0, Signals{}, SkipInactive
	}
	if related(a, b) || related(b, a) {
		return 0, Signals{}, SkipRelated
	}
	shared, conflicting := compareIDs(a, b)
	if conflicting {
		return 0, Signals{}, SkipConflictingIDs
	}
	sameProvider := len(a.Records) > 0 && slices.Equal(a.Providers(), b.Providers())
	if sameProvider && opts.SkipSameProvider {
		return 0, Signals{}, SkipSameProvider
	}

	s := Signals{
		YearA: a.Year, YearB: b.Year, TypeA: a.Type, TypeB: b.Type,
		LanguageA: a.Language, LanguageB: b.Language, SharedIDs: shared, SameProvider: sameProvider,
	}
	s.Title, s.TitleA, s.TitleB = bestTitle(a.Titles, b.Titles)
	s.People, s.SharedPeople = people(a.People, b.People)

	// Weighted evidence; neutral (0.5) when a side doesn't know.
	score := 0.5 * s.Title
	switch {
	case s.People < 0:
		score += 0.2 * 0.5
	default:
		score += 0.2 * math.Min(1, s.People*2) // half the people in common is plenty
	}
	score += 0.12 * yearMatch(a.Year, b.Year)
	score += 0.1 * same(a.Type, b.Type, "unknown")
	score += 0.08 * same(strings.ToLower(a.Language), strings.ToLower(b.Language), "")
	if len(shared) > 0 {
		score += 0.3
	}
	if sameProvider {
		score -= 0.05
	}
	return clamp(score), s, ""
}

func clamp(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// related reports whether a relation of a points at one of b's records.
func related(a, b *Features) bool {
	for _, r := range a.Relations {
		if slices.Contains(b.Records, r) || slices.Contains(b.ExternalIDs, r) {
			return true
		}
	}
	return false
}

// compareIDs finds IDs at other sites both report, and whether both report
// different IDs at a site where a work has one ID (a strong "different").
// Sites whose records are the entries' own sources are left out: two
// records of one provider differ by definition.
func compareIDs(a, b *Features) (shared []string, conflicting bool) {
	own := map[string]bool{}
	for _, r := range append(append([]Ref{}, a.Records...), b.Records...) {
		own[r.Provider] = true
	}
	ids := func(f *Features) map[string][]string {
		m := map[string][]string{}
		for _, r := range f.ExternalIDs {
			if !own[r.Provider] {
				m[r.Provider] = append(m[r.Provider], r.ID)
			}
		}
		return m
	}
	ia, ib := ids(a), ids(b)
	for site, as := range ia {
		bs, ok := ib[site]
		if !ok {
			continue
		}
		overlap := false
		for _, x := range as {
			if slices.Contains(bs, x) {
				overlap = true
				shared = append(shared, Ref{Provider: site, ID: x}.String())
			}
		}
		if !overlap {
			conflicting = true
		}
	}
	slices.Sort(shared)
	return shared, conflicting
}

// bestTitle returns the highest similarity between any two titles.
func bestTitle(as, bs []string) (float64, string, string) {
	best, ba, bb := 0.0, "", ""
	for _, x := range as {
		for _, y := range bs {
			sim := Similarity(x, y)
			if sim > best {
				best, ba, bb = sim, x, y
			}
			if best == 1 {
				return best, ba, bb
			}
		}
	}
	return best, ba, bb
}

// people returns the share of people in common (of the smaller side) and
// who they are; -1 when a side credits nobody.
func people(as, bs []string) (float64, []string) {
	if len(as) == 0 || len(bs) == 0 {
		return -1, nil
	}
	var shared []string
	for _, x := range as {
		if slices.Contains(bs, x) && !slices.Contains(shared, x) {
			shared = append(shared, x)
		}
	}
	return float64(len(shared)) / float64(min(len(as), len(bs))), shared
}

func yearMatch(a, b int) float64 {
	switch d := abs(a - b); {
	case a == 0 || b == 0:
		return 0.5
	case d == 0:
		return 1
	case d == 1:
		return 0.7
	case d <= 3:
		return 0.3
	}
	return 0
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func same(a, b, unknown string) float64 {
	switch {
	case a == "" || b == "" || a == unknown || b == unknown:
		return 0.5
	case a == b:
		return 1
	}
	return 0
}

// Similarity is pg_trgm's similarity of two normalized strings: shared
// trigrams over all trigrams, with words padded as PostgreSQL does.
func Similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	ta, tb := trigrams(a), trigrams(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	common := 0
	for g := range ta {
		if tb[g] {
			common++
		}
	}
	return float64(common) / float64(len(ta)+len(tb)-common)
}

func trigrams(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(s) {
		r := []rune("  " + w + " ")
		for i := 0; i+3 <= len(r); i++ {
			out[string(r[i:i+3])] = true
		}
	}
	return out
}

// PairHash identifies what a pair's comparison saw: both entries' content
// and the scoring version.
func PairHash(a, b *Features) []byte {
	if b.ID.String() < a.ID.String() {
		a, b = b, a
	}
	h := sha256.New()
	h.Write(a.ContentHash)
	h.Write(b.ContentHash)
	h.Write([]byte{ScoreVersion})
	return h.Sum(nil)
}
