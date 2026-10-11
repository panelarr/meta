// SPDX-License-Identifier: GPL-3.0-or-later

package duplicates

import (
	"math"
	"testing"

	"github.com/google/uuid"
)

func feat(title string, mod func(f *Features)) *Features {
	f := &Features{
		ID: uuid.New(), State: "active", Title: title, Type: "manga", Language: "ja", Year: 2010,
		Titles: []string{title}, People: []string{"hana imaginary"},
		Records: []Ref{{"alpha", uuid.NewString()}},
	}
	if mod != nil {
		mod(f)
	}
	return f
}

func TestSimilarityMatchesTrigrams(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"lantern keeper", "lantern keeper", 1},
		{"abc", "abd", 0.333}, // pg_trgm: similarity('abc','abd') = 0.333
		{"word", "", 0},
	}
	for _, c := range cases {
		if got := Similarity(c.a, c.b); math.Abs(got-c.want) > 0.01 {
			t.Errorf("similarity(%q, %q) = %.3f, want %.3f", c.a, c.b, got, c.want)
		}
	}
}

func TestScore(t *testing.T) {
	base := feat("quiet orbit", nil)
	cases := []struct {
		name     string
		b        *Features
		min, max float64
		skip     string
	}{
		{"same title, people, year", feat("quiet orbit", nil), 0.9, 1, ""},
		{"near title, same people", feat("quiet orbits", nil), 0.75, 1, ""},
		{"same title, other people, other year, other type", feat("quiet orbit", func(f *Features) {
			f.People, f.Year, f.Type = []string{"someone else"}, 1985, "novel"
		}), 0, 0.6, ""},
		{"different title", feat("salt and iron", func(f *Features) { f.People = []string{"someone else"} }), 0, 0.4, ""},
		{"no people on one side is neutral", feat("quiet orbit", func(f *Features) { f.People = nil }), 0.8, 1, ""},
		{"shared ID elsewhere", feat("orbit (quiet)", func(f *Features) {
			f.ExternalIDs = []Ref{{"anilist", "42"}}
		}), 0, 1, ""},
		{"conflicting IDs elsewhere", feat("quiet orbit", func(f *Features) {
			f.ExternalIDs = []Ref{{"anilist", "43"}}
		}), 0, 0, SkipConflictingIDs},
		{"related", feat("quiet orbit", func(f *Features) {
			f.Relations = []Ref{base.Records[0]}
		}), 0, 0, SkipRelated},
		{"merged", feat("quiet orbit", func(f *Features) { f.State = "merged" }), 0, 0, SkipInactive},
	}
	base.ExternalIDs = []Ref{{"anilist", "42"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, sig, skip := Score(base, c.b, Options{})
			if skip != c.skip {
				t.Fatalf("skip %q, want %q", skip, c.skip)
			}
			if skip == "" && (got < c.min || got > c.max) {
				t.Errorf("score %.2f, want %.2f..%.2f (%+v)", got, c.min, c.max, sig)
			}
		})
	}

	// A shared ID elsewhere lifts a pair with weak titles.
	withID, sig, _ := Score(base, feat("orbit (quiet)", func(f *Features) { f.ExternalIDs = []Ref{{"anilist", "42"}} }), Options{})
	without, _, _ := Score(base, feat("orbit (quiet)", nil), Options{})
	if withID-without < 0.25 || len(sig.SharedIDs) != 1 || sig.SharedIDs[0] != "anilist:42" {
		t.Errorf("shared ID: %.2f vs %.2f (%v)", withID, without, sig.SharedIDs)
	}
}

func TestSameProvider(t *testing.T) {
	a := feat("lantern keeper", nil)
	b := feat("lantern keeper", nil) // both "alpha" records
	got, sig, skip := Score(a, b, Options{})
	if skip != "" || !sig.SameProvider || got > 0.96 {
		t.Fatalf("same provider: %.2f %+v %q", got, sig, skip)
	}
	if _, _, skip := Score(a, b, Options{SkipSameProvider: true}); skip != SkipSameProvider {
		t.Fatalf("strict rule: %q", skip)
	}
	// The provider's own IDs never conflict: they differ by definition.
	other := feat("lantern keeper", func(f *Features) { f.Records = []Ref{{"beta", "7"}} })
	if _, sig, skip := Score(a, other, Options{SkipSameProvider: true}); skip != "" || sig.SameProvider {
		t.Fatalf("different providers: %+v %q", sig, skip)
	}
}

func TestPairHashIsOrderFree(t *testing.T) {
	a, b := feat("x", func(f *Features) { f.ContentHash = []byte{1} }), feat("y", func(f *Features) { f.ContentHash = []byte{2} })
	if string(PairHash(a, b)) != string(PairHash(b, a)) {
		t.Fatal("hash depends on order")
	}
	b2 := *b
	b2.ContentHash = []byte{3}
	if string(PairHash(a, b)) == string(PairHash(a, &b2)) {
		t.Fatal("hash ignores content")
	}
}
