// SPDX-License-Identifier: GPL-3.0-or-later

package duplicates_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/duplicates"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/storetest"
)

func rec(provider, id, title string, mod func(e *metadatav1.ProviderEntry)) *metadatav1.ProviderEntry {
	e := &metadatav1.ProviderEntry{
		Ref:    &metadatav1.ExternalId{Provider: provider, Id: id},
		State:  metadatav1.EntryState_ENTRY_STATE_ACTIVE,
		Type:   metadatav1.EntryType_ENTRY_TYPE_MANGA,
		Titles: []*metadatav1.Title{{Text: title, Language: "en", IsPrimary: true}},
		Dates:  &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 2012}},
	}
	if mod != nil {
		mod(e)
	}
	return e
}

func credit(name string) []*metadatav1.Credit {
	return []*metadatav1.Credit{{Person: &metadatav1.Person{Name: name}, Role: metadatav1.PersonRole_PERSON_ROLE_AUTHOR}}
}

type fixture struct {
	st *store.Store
	w  *catalog.Writer
}

func seed(t *testing.T) *fixture {
	t.Helper()
	st := storetest.New(t)
	w := catalog.NewWriter(st, catalog.Options{Priority: catalog.Priority{Fallback: []string{"alpha", "beta"}}, DisplayLanguage: "en"})
	apply := func(provider string, recs ...*metadatav1.ProviderEntry) {
		if _, err := w.Apply(context.Background(), catalog.Batch{Provider: provider, Upserts: recs}); err != nil {
			t.Fatal(err)
		}
	}
	apply("alpha",
		// The provider's own duplicate: same work, two records.
		rec("alpha", "1", "Lantern Keeper", func(e *metadatav1.ProviderEntry) { e.Credits = credit("Hana Imaginary") }),
		rec("alpha", "2", "The Lantern Keeper", func(e *metadatav1.ProviderEntry) { e.Credits = credit("Hana Imaginary") }),
		// Same title, but a sequel of 1: related, never a duplicate.
		rec("alpha", "3", "Lantern Keeper", func(e *metadatav1.ProviderEntry) {
			e.Credits = credit("Kai Fictional")
			e.Dates = &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 2016}}
			e.Relations = []*metadatav1.Relation{{Type: metadatav1.RelationType_RELATION_TYPE_PREQUEL, Target: &metadatav1.ExternalId{Provider: "alpha", Id: "1"}}}
		}),
		// A different work with the same title: other author, other IDs.
		rec("alpha", "4", "Salt and Iron", func(e *metadatav1.ProviderEntry) {
			e.Credits = credit("Ren Invented")
			e.ExternalIds = []*metadatav1.ExternalId{{Provider: "anilist", Id: "100"}}
		}),
		rec("alpha", "5", "Salt and Iron", func(e *metadatav1.ProviderEntry) {
			e.Credits = credit("Mo Madeup")
			e.Dates = &metadatav1.Dates{Start: &metadatav1.PartialDate{Year: 1990}}
			e.ExternalIds = []*metadatav1.ExternalId{{Provider: "anilist", Id: "200"}}
		}),
		rec("alpha", "6", "Completely Unrelated Story", nil),
	)
	return &fixture{st: st, w: w}
}

func (f *fixture) entry(t *testing.T, provider, id string) uuid.UUID {
	t.Helper()
	var e uuid.UUID
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT entry_id FROM provider_entries WHERE provider = $1 AND external_id = $2`, provider, id).Scan(&e); err != nil {
		t.Fatal(err)
	}
	return e
}

type candidate struct {
	a, b    uuid.UUID
	status  string
	score   float64
	signals duplicates.Signals
}

func (f *fixture) candidates(t *testing.T) []candidate {
	t.Helper()
	rows, err := f.st.Pool.Query(context.Background(), `SELECT entry_a, entry_b, status, score, signals FROM duplicate_candidates ORDER BY score DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		var sig []byte
		if err := rows.Scan(&c.a, &c.b, &c.status, &c.score, &sig); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(sig, &c.signals)
		out = append(out, c)
	}
	return out
}

func is(c candidate, x, y uuid.UUID) bool { return (c.a == x && c.b == y) || (c.a == y && c.b == x) }

func TestFindConfirmAndDismiss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := seed(t)
	e1, e2, e3, e4, e5 := f.entry(t, "alpha", "1"), f.entry(t, "alpha", "2"), f.entry(t, "alpha", "3"), f.entry(t, "alpha", "4"), f.entry(t, "alpha", "5")
	if e1 == e2 {
		t.Fatal("the provider's two records were linked by ingest")
	}

	finder := duplicates.NewFinder(f.st.Pool, duplicates.FinderOptions{})
	rep, err := finder.Run(ctx, time.Time{}, uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cands := f.candidates(t)
	if len(cands) != 1 || !is(cands[0], e1, e2) || rep.New != 1 {
		t.Fatalf("candidates: %+v (report %+v)", cands, rep)
	}
	if c := cands[0]; !c.signals.SameProvider || c.signals.People != 1 || c.score < 0.7 {
		t.Fatalf("pair: %+v", c)
	}
	if rep.Skipped[duplicates.SkipRelated] == 0 || rep.Skipped[duplicates.SkipConflictingIDs] == 0 {
		t.Fatalf("skips: %+v", rep.Skipped)
	}
	for _, c := range cands {
		if is(c, e1, e3) || is(c, e2, e3) || is(c, e4, e5) {
			t.Fatalf("a related or distinct pair was kept: %+v", c)
		}
	}

	// Running again changes nothing.
	if rep, err := finder.Run(ctx, time.Time{}, uuid.New(), nil); err != nil || rep.New != 0 || len(f.candidates(t)) != 1 {
		t.Fatalf("second run: %+v %v", rep, err)
	}

	// Confirm: 2 joins 1, with both titles, and its ID redirects.
	if _, err := f.w.MergeEntries(ctx, e2, e1, "confirmed duplicate"); err != nil {
		t.Fatal(err)
	}
	var titles int
	_ = f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM entry_titles WHERE entry_id = $1`, e1).Scan(&titles)
	var state, redirect string
	_ = f.st.Pool.QueryRow(ctx, `SELECT state FROM entries WHERE id = $1`, e2).Scan(&state)
	_ = f.st.Pool.QueryRow(ctx, `SELECT to_id::text FROM entry_redirects WHERE from_id = $1`, e2).Scan(&redirect)
	if titles != 2 || state != "merged" || redirect != e1.String() || f.entry(t, "alpha", "2") != e1 {
		t.Fatalf("after confirm: titles %d, state %s, redirect %s", titles, state, redirect)
	}
	// A later sync of both records keeps them together.
	if _, err := f.w.Apply(ctx, catalog.Batch{Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{
		rec("alpha", "1", "Lantern Keeper", func(e *metadatav1.ProviderEntry) { e.Credits = credit("Hana Imaginary") }),
		rec("alpha", "2", "The Lantern Keeper!", func(e *metadatav1.ProviderEntry) { e.Credits = credit("Hana Imaginary") }),
	}}); err != nil {
		t.Fatal(err)
	}
	if f.entry(t, "alpha", "1") != f.entry(t, "alpha", "2") {
		t.Fatal("a re-sync split the confirmed duplicate")
	}
	// The pair is moot now: the next run drops it.
	if _, err := finder.Run(ctx, time.Time{}, uuid.New(), nil); err != nil || len(f.candidates(t)) != 0 {
		t.Fatalf("after the merge: %+v %v", f.candidates(t), err)
	}
	if _, err := f.w.MergeEntries(ctx, e2, e1, "again"); err == nil {
		t.Fatal("merged a merged entry")
	}
}

func TestDismissedPairsDontComeBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := seed(t)
	finder := duplicates.NewFinder(f.st.Pool, duplicates.FinderOptions{})
	if _, err := finder.Run(ctx, time.Time{}, uuid.New(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE duplicate_candidates SET status = 'dismissed', decided_at = now()`); err != nil {
		t.Fatal(err)
	}
	rep, err := finder.Run(ctx, time.Time{}, uuid.New(), nil)
	cands := f.candidates(t)
	if err != nil || rep.New != 0 || len(cands) != 1 || cands[0].status != "dismissed" {
		t.Fatalf("after dismissing: %+v %+v %v", cands, rep, err)
	}
}

func TestIncrementalRunsOnlyLookAtChangedEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := seed(t)
	finder := duplicates.NewFinder(f.st.Pool, duplicates.FinderOptions{})
	since := time.Now()
	rep, err := finder.Run(ctx, since, uuid.New(), nil)
	if err != nil || rep.Entries != 0 {
		t.Fatalf("nothing changed: %+v %v", rep, err)
	}
	// A new record from another provider with the same title and author.
	if _, err := f.w.Apply(ctx, catalog.Batch{Provider: "beta", Upserts: []*metadatav1.ProviderEntry{
		rec("beta", "9", "Completely Unrelated Story", nil),
	}}); err != nil {
		t.Fatal(err)
	}
	rep, err = finder.Run(ctx, since, uuid.New(), nil)
	if err != nil || rep.Entries != 1 || rep.New != 1 {
		t.Fatalf("one new entry: %+v %v", rep, err)
	}
}
