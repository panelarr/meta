// SPDX-License-Identifier: GPL-3.0-or-later

package catalog_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/internal/store/storetest"
)

func record(provider, id, title string, xs ...string) *metadatav1.ProviderEntry {
	e := &metadatav1.ProviderEntry{
		Ref:    &metadatav1.ExternalId{Provider: provider, Id: id},
		Type:   metadatav1.EntryType_ENTRY_TYPE_MANGA,
		Titles: []*metadatav1.Title{{Text: title, Language: "en", IsPrimary: true}},
		State:  metadatav1.EntryState_ENTRY_STATE_ACTIVE,
		Credits: []*metadatav1.Credit{{
			Person: &metadatav1.Person{Name: "Aki Tanaka"}, Role: metadatav1.PersonRole_PERSON_ROLE_AUTHOR,
		}},
		Tags: []*metadatav1.TagAssignment{{Tag: &metadatav1.Tag{Name: "Romance", IsGenre: true}}},
	}
	for i := 0; i+1 < len(xs); i += 2 {
		e.ExternalIds = append(e.ExternalIds, &metadatav1.ExternalId{Provider: xs[i], Id: xs[i+1]})
	}
	return e
}

func newWriter(t *testing.T) (*catalog.Writer, *store.Store) {
	t.Helper()
	s := storetest.New(t)
	return catalog.NewWriter(s, catalog.Options{
		Priority:        catalog.Priority{Fallback: []string{"alpha", "beta"}},
		DisplayLanguage: "en",
	}), s
}

func entryOf(t *testing.T, s *store.Store, provider, id string) db.Entry {
	t.Helper()
	pe, err := s.GetProviderEntry(context.Background(), db.GetProviderEntryParams{Provider: provider, ExternalID: id})
	if err != nil {
		t.Fatalf("provider entry %s:%s: %v", provider, id, err)
	}
	e, err := s.GetEntry(context.Background(), pe.EntryID.UUID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func run(t *testing.T) uuid.NullUUID {
	t.Helper()
	return uuid.NullUUID{UUID: uuid.Must(uuid.NewV7()), Valid: true}
}

func TestApplyIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, s := newWriter(t)
	batch := catalog.Batch{Provider: "alpha", Run: run(t), Upserts: []*metadatav1.ProviderEntry{
		record("alpha", "1", "First Light", "anilist", "10"),
		record("alpha", "2", "Second Wind"),
	}}
	res, err := w.Apply(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 2 || res.EntriesChanged != 2 {
		t.Fatalf("first apply: %+v", res)
	}
	before := entryOf(t, s, "alpha", "1")
	if before.Title != "First Light" || before.Type != "manga" || before.State != "active" {
		t.Fatalf("entry: %+v", before)
	}

	batch.Run = run(t)
	res, err = w.Apply(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 0 || res.Unchanged != 2 || res.EntriesChanged != 0 {
		t.Fatalf("second apply changed things: %+v", res)
	}
	after := entryOf(t, s, "alpha", "1")
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("entry was rewritten")
	}
	// The shared person and tag exist once.
	var persons, tags int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM persons`).Scan(&persons)
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM tags`).Scan(&tags)
	if persons != 1 || tags != 1 {
		t.Fatalf("persons %d, tags %d", persons, tags)
	}
}

func TestLinkingAcrossProviders(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, s := newWriter(t)
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{
		record("alpha", "1", "First Light", "anilist", "10"),
		// Same AniList ID, same provider: stays a separate entry.
		record("alpha", "9", "First Light (duplicate)", "anilist", "10"),
	}}); err != nil {
		t.Fatal(err)
	}
	beta := record("beta", "x", "first light", "anilist", "10")
	beta.Type = metadatav1.EntryType_ENTRY_TYPE_MANHWA
	beta.OriginalLanguage = "ko"
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "beta", Upserts: []*metadatav1.ProviderEntry{beta}}); err != nil {
		t.Fatal(err)
	}
	a, b, dup := entryOf(t, s, "alpha", "1"), entryOf(t, s, "beta", "x"), entryOf(t, s, "alpha", "9")
	if a.ID != b.ID {
		t.Fatal("beta's record did not join alpha's entry")
	}
	if dup.ID == a.ID {
		t.Fatal("two records of one provider were joined")
	}
	// alpha wins the type; beta fills the language alpha lacks.
	if a := entryOf(t, s, "alpha", "1"); a.Type != "manga" || a.OriginalLanguage != "ko" {
		t.Fatalf("merged fields: type %q language %q", a.Type, a.OriginalLanguage)
	}
	found, err := s.LookupExternalID(ctx, db.LookupExternalIDParams{Provider: "beta", ExternalID: "x"})
	if err != nil || len(found) != 1 || found[0].EntryID != a.ID {
		t.Fatalf("lookup beta:x: %v %v", found, err)
	}
}

func TestProviderMergeRedirects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, s := newWriter(t)
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{
		record("alpha", "1", "Kept"),
		record("alpha", "3", "Old Duplicate"),
	}}); err != nil {
		t.Fatal(err)
	}
	old := entryOf(t, s, "alpha", "3")
	merged := record("alpha", "3", "Old Duplicate")
	merged.State = metadatav1.EntryState_ENTRY_STATE_MERGED
	merged.MergedInto = &metadatav1.ExternalId{Provider: "alpha", Id: "1"}
	if _, err := w.Apply(ctx, catalog.Batch{
		Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{merged},
		Merges: []catalog.Merge{{Merged: "3", Into: "1"}},
	}); err != nil {
		t.Fatal(err)
	}
	kept := entryOf(t, s, "alpha", "1")
	retired, err := s.GetEntry(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retired.State != "merged" || retired.MergedInto.UUID != kept.ID {
		t.Fatalf("retired entry: %+v", retired)
	}
	to, err := s.GetRedirect(ctx, old.ID)
	if err != nil || to != kept.ID {
		t.Fatalf("redirect: %v %v", to, err)
	}
	// The old provider ID still finds the kept entry.
	found, err := s.LookupExternalID(ctx, db.LookupExternalIDParams{Provider: "alpha", ExternalID: "3"})
	if err != nil || len(found) == 0 || found[0].EntryID != kept.ID {
		t.Fatalf("lookup alpha:3: %v %v", found, err)
	}
	// Merging again changes nothing.
	res, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{merged}, Merges: []catalog.Merge{{Merged: "3", Into: "1"}}})
	if err != nil || res.Changed != 0 || res.EntriesChanged != 0 {
		t.Fatalf("repeat merge: %+v %v", res, err)
	}
}

func TestMergeBeforeTargetArrives(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, s := newWriter(t)
	merged := record("alpha", "3", "Old")
	merged.State = metadatav1.EntryState_ENTRY_STATE_MERGED
	merged.MergedInto = &metadatav1.ExternalId{Provider: "alpha", Id: "7"}
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{merged}, Merges: []catalog.Merge{{Merged: "3", Into: "7"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{record("alpha", "7", "New")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Reconcile(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if a, b := entryOf(t, s, "alpha", "3"), entryOf(t, s, "alpha", "7"); a.ID != b.ID || b.State != "active" {
		t.Fatalf("not reconciled: %s %s (%s)", a.ID, b.ID, b.State)
	}
}

func TestSnapshotRemovalAndReturn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, s := newWriter(t)
	r1 := run(t)
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Run: r1, Upserts: []*metadatav1.ProviderEntry{
		record("alpha", "1", "Stays"), record("alpha", "2", "Goes"),
	}}); err != nil {
		t.Fatal(err)
	}
	r2 := run(t)
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Run: r2, Upserts: []*metadatav1.ProviderEntry{record("alpha", "1", "Stays")}}); err != nil {
		t.Fatal(err)
	}
	n, err := w.FinishSnapshot(ctx, "alpha", r2.UUID)
	if err != nil || n != 1 {
		t.Fatalf("finish snapshot: %d %v", n, err)
	}
	if e := entryOf(t, s, "alpha", "2"); e.State != "removed" || e.Title != "Goes" {
		t.Fatalf("removed entry: %+v", e)
	}
	if e := entryOf(t, s, "alpha", "1"); e.State != "active" {
		t.Fatalf("kept entry: %+v", e)
	}
	// It comes back unchanged in a later snapshot: active again.
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Run: run(t), Upserts: []*metadatav1.ProviderEntry{record("alpha", "2", "Goes")}}); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "alpha", "2"); e.State != "active" {
		t.Fatalf("returned entry: %+v", e)
	}
	// An explicit removal event.
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Removals: []string{"2"}}); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "alpha", "2"); e.State != "removed" {
		t.Fatalf("removed by event: %+v", e)
	}
}

func TestLocksSurviveRebuilds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, s := newWriter(t)
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{record("alpha", "1", "Provider Title")}}); err != nil {
		t.Fatal(err)
	}
	e := entryOf(t, s, "alpha", "1")
	if err := s.SetFieldLock(ctx, db.SetFieldLockParams{EntryID: e.ID, Field: "title", Value: []byte(`"Curated"`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFieldLock(ctx, db.SetFieldLockParams{EntryID: e.ID, Field: "type", Value: []byte(`{"type":"ENTRY_TYPE_ONE_SHOT"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Rebuild(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	// A provider update doesn't undo the locks.
	upd := record("alpha", "1", "Provider Title v2")
	upd.Type = metadatav1.EntryType_ENTRY_TYPE_NOVEL
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{upd}}); err != nil {
		t.Fatal(err)
	}
	got := entryOf(t, s, "alpha", "1")
	if got.Title != "Curated" || got.Type != "one_shot" {
		t.Fatalf("locks lost: title %q type %q", got.Title, got.Type)
	}
	if err := s.DeleteFieldLock(ctx, db.DeleteFieldLockParams{EntryID: e.ID, Field: "type"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Rebuild(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if got := entryOf(t, s, "alpha", "1"); got.Type != "novel" {
		t.Fatalf("unlocked type: %q", got.Type)
	}
}
