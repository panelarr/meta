// SPDX-License-Identifier: GPL-3.0-or-later

package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/internal/store/storetest"
)

func TestMigrateUpDownUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.Open(t)

	applied, err := s.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 {
		t.Fatal("no migrations applied")
	}
	// Running again is a no-op.
	again, err := s.Migrate(ctx)
	if err != nil || len(again) != 0 {
		t.Fatalf("second migrate: %v %v", again, err)
	}
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range st {
		if !m.Applied {
			t.Fatalf("migration %d not applied", m.Version)
		}
	}
	for range applied {
		if _, err := s.MigrateDown(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename = 'entries'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("entries table still there after down: %d %v", n, err)
	}
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

// The tests run as a role without superuser rights, like meta in
// production. This makes the requirement visible.
func TestRunsWithoutSuperuser(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	var super bool
	if err := s.Pool.QueryRow(context.Background(), `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
		t.Fatal(err)
	}
	if super {
		t.Skip("the test server connects as a superuser; point META_TEST_DATABASE_URL at an ordinary role to check this")
	}
}

func newEntry(t *testing.T, s *store.Store) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if err := s.InsertEntry(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEntryPartsAndExternalIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	id := newEntry(t, s)

	err := s.UpdateEntry(ctx, db.UpdateEntryParams{
		ID: id, State: "active", Title: "First Light", Type: "manga", Status: "releasing",
		ContentRating: "safe", OriginalLanguage: "ja", Year: pgtype.Int4{Int32: 2015, Valid: true},
		Adaptations: []byte("[]"), Items: []byte("[]"), Licenses: []byte("[]"), Stats: []byte("[]"),
		FieldSources: []byte(`{"title":"fake"}`), SearchText: "first light hajimari no hikari",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEntryTitles(ctx, []db.InsertEntryTitlesParams{
		{EntryID: id, Position: 0, Text: "First Light", Norm: "first light", Language: "en", Kind: "official", IsPrimary: true, Traits: []string{}},
		{EntryID: id, Position: 1, Text: "Hajimari no Hikari", Norm: "hajimari no hikari", Language: "ja-Latn", Kind: "romanized", Traits: []string{"old"}},
	}); err != nil {
		t.Fatal(err)
	}
	titles, err := s.ListEntryTitles(ctx, id)
	if err != nil || len(titles) != 2 || titles[1].Traits[0] != "old" {
		t.Fatalf("titles: %v %v", titles, err)
	}

	if _, err := s.InsertEntryExternalIDs(ctx, []db.InsertEntryExternalIDsParams{
		{Provider: "fake", ExternalID: "1", EntryID: id, Source: "fake:1"},
		{Provider: "anilist", ExternalID: "30013", EntryID: id, Source: "fake:1"},
	}); err != nil {
		t.Fatal(err)
	}
	found, err := s.FindEntriesByExternalIDs(ctx, db.FindEntriesByExternalIDsParams{
		Providers:   []string{"mal", "anilist"},
		ExternalIds: []string{"1", "30013"},
	})
	if err != nil || len(found) != 1 || found[0].EntryID != id {
		t.Fatalf("find by external ids: %v %v", found, err)
	}
	look, err := s.LookupExternalID(ctx, db.LookupExternalIDParams{Provider: "anilist", ExternalID: "30013"})
	if err != nil || len(look) != 1 || look[0].EntryID != id {
		t.Fatalf("lookup: %v %v", look, err)
	}

	// Relations resolve their target through external IDs.
	other := newEntry(t, s)
	if _, err := s.InsertEntryExternalIDs(ctx, []db.InsertEntryExternalIDsParams{{Provider: "fake", ExternalID: "2", EntryID: other}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEntryRelations(ctx, []db.InsertEntryRelationsParams{
		{EntryID: id, Position: 0, Type: "sequel", TargetProvider: "fake", TargetID: "2"},
		{EntryID: id, Position: 1, Type: "prequel", TargetProvider: "fake", TargetID: "404"},
	}); err != nil {
		t.Fatal(err)
	}
	rels, err := s.ListEntryRelations(ctx, id)
	if err != nil || len(rels) != 2 {
		t.Fatalf("relations: %v %v", rels, err)
	}
	if rels[0].TargetEntryID != other.String() || rels[1].TargetEntryID != "" {
		t.Fatalf("relation targets: %q %q", rels[0].TargetEntryID, rels[1].TargetEntryID)
	}

	// Deleting an entry removes its parts.
	if err := s.DeleteEntry(ctx, other); err != nil {
		t.Fatal(err)
	}
	rels, _ = s.ListEntryRelations(ctx, id)
	if rels[0].TargetEntryID != "" {
		t.Fatal("relation still resolves to a deleted entry")
	}
}

func TestProviderEntriesAndRemovals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	run1 := uuid.Must(uuid.NewV7())
	run2 := uuid.Must(uuid.NewV7())
	entry := newEntry(t, s)

	for _, id := range []string{"1", "2", "3"} {
		err := s.UpsertProviderEntry(ctx, db.UpsertProviderEntryParams{
			Provider: "fake", ExternalID: id, EntryID: uuid.NullUUID{UUID: entry, Valid: id == "1"}, State: "active",
			Record: []byte(`{}`), ContentHash: []byte{1}, LastSeenRun: uuid.NullUUID{UUID: run1, Valid: true},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	states, err := s.GetProviderEntryStates(ctx, db.GetProviderEntryStatesParams{Provider: "fake", ExternalIds: []string{"1", "3", "9"}})
	if err != nil || len(states) != 2 {
		t.Fatalf("states: %v %v", states, err)
	}
	// Run 2 sees records 1 and 2 only.
	if err := s.TouchProviderEntries(ctx, db.TouchProviderEntriesParams{RunID: uuid.NullUUID{UUID: run2, Valid: true}, Provider: "fake", ExternalIds: []string{"1", "2"}}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.MarkUnseenRemoved(ctx, db.MarkUnseenRemovedParams{Provider: "fake", RunID: run2})
	if err != nil || len(removed) != 1 {
		t.Fatalf("mark removed: %v %v", removed, err)
	}
	pe, err := s.GetProviderEntry(ctx, db.GetProviderEntryParams{Provider: "fake", ExternalID: "3"})
	if err != nil || pe.State != "removed" || pe.RemovedAt == nil {
		t.Fatalf("record 3: %+v %v", pe, err)
	}
	// Seen again: back to active.
	if err := s.UpsertProviderEntry(ctx, db.UpsertProviderEntryParams{
		Provider: "fake", ExternalID: "3", State: "active", Record: []byte(`{}`), ContentHash: []byte{2},
	}); err != nil {
		t.Fatal(err)
	}
	pe, _ = s.GetProviderEntry(ctx, db.GetProviderEntryParams{Provider: "fake", ExternalID: "3"})
	if pe.State != "active" || pe.RemovedAt != nil {
		t.Fatalf("record 3 not restored: %+v", pe)
	}
	// A provider merge.
	got, err := s.SetProviderEntryMerged(ctx, db.SetProviderEntryMergedParams{
		Provider: "fake", ExternalID: "1", MergedInto: pgtype.Text{String: "2", Valid: true}, RunID: uuid.NullUUID{UUID: run2, Valid: true},
	})
	if err != nil || got.UUID != entry {
		t.Fatalf("merged: %v %v", got, err)
	}
	has, err := s.EntryHasProviderRecord(ctx, db.EntryHasProviderRecordParams{EntryID: uuid.NullUUID{UUID: entry, Valid: true}, Provider: "fake", ExternalID: "x"})
	if err != nil || has {
		t.Fatalf("entry should hold no active fake record now: %v %v", has, err)
	}
}

func TestRedirects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	a, b, c := newEntry(t, s), newEntry(t, s), newEntry(t, s)
	if err := s.InsertRedirect(ctx, db.InsertRedirectParams{FromID: a, ToID: b, Reason: "merged"}); err != nil {
		t.Fatal(err)
	}
	// b is merged into c later: a must point at c directly.
	if err := s.RepointRedirects(ctx, db.RepointRedirectsParams{FromID: b, ToID: c}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertRedirect(ctx, db.InsertRedirectParams{FromID: b, ToID: c}); err != nil {
		t.Fatal(err)
	}
	for _, from := range []uuid.UUID{a, b} {
		to, err := s.GetRedirect(ctx, from)
		if err != nil || to != c {
			t.Fatalf("redirect %s: %s %v", from, to, err)
		}
	}
	if _, err := s.GetRedirect(ctx, c); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("no redirect expected for c: %v", err)
	}
}

func TestTagsSharedByKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	up := func(key, name string, genre bool, path ...string) uuid.UUID {
		if path == nil {
			path = []string{}
		}
		id, err := s.UpsertTag(ctx, db.UpsertTagParams{
			ID: uuid.Must(uuid.NewV7()), Key: key, Name: name, NameKey: "romance", Path: path, IsGenre: genre, ContentRating: "safe", Data: []byte(`{}`),
		})
		if errors.Is(err, pgx.ErrNoRows) { // unchanged: nothing returned
			id, err = s.GetTagIDByKey(ctx, key)
		}
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	genre := up("genre/romance", "Romance", true)
	if again := up("genre/romance", "Romance", true); again != genre {
		t.Fatal("same key gave a new tag")
	}
	// A tag with the same name under a parent is a different tag.
	tag := up("tag/themes/romance", "Romance", false, "Themes")
	if tag == genre {
		t.Fatal("a tag and a genre with the same name were joined")
	}
	e := newEntry(t, s)
	if _, err := s.InsertEntryTags(ctx, []db.InsertEntryTagsParams{{EntryID: e, TagID: genre, Weight: "core"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecountTags(ctx); err != nil {
		t.Fatal(err)
	}
	tags, err := s.ListTags(ctx, db.ListTagsParams{GenresOnly: true, Prefix: "rom", Lim: 10})
	if err != nil || len(tags) != 1 || tags[0].EntryCount != 1 || !tags[0].IsGenre {
		t.Fatalf("tags: %+v %v", tags, err)
	}
	if n, err := s.DeleteUnusedTags(ctx); err != nil || n != 1 {
		t.Fatalf("unused tags removed: %d %v", n, err)
	}
}

func TestPersonsNameOnlyAreShared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	id := uuid.Must(uuid.NewV7())
	if err := s.InsertPerson(ctx, db.InsertPersonParams{ID: id, Name: "Aki Tanaka", NameKey: "aki tanaka", Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// A second insert with the same key is ignored.
	if err := s.InsertPerson(ctx, db.InsertPersonParams{ID: uuid.Must(uuid.NewV7()), Name: "Aki Tanaka", NameKey: "aki tanaka", Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindNameOnlyPerson(ctx, "aki tanaka")
	if err != nil || got != id {
		t.Fatalf("name-only person: %v %v", got, err)
	}
	// People with IDs are separate, even with the same name.
	withID := uuid.Must(uuid.NewV7())
	if err := s.InsertPerson(ctx, db.InsertPersonParams{ID: withID, Name: "Aki Tanaka", NameKey: "aki tanaka", HasIds: true, Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPersonExternalID(ctx, db.InsertPersonExternalIDParams{Provider: "anilist", ExternalID: "77", PersonID: withID}); err != nil {
		t.Fatal(err)
	}
	p, err := s.FindPersonByExternalIDs(ctx, db.FindPersonByExternalIDsParams{Providers: []string{"anilist"}, ExternalIds: []string{"77"}})
	if err != nil || p != withID {
		t.Fatalf("person by id: %v %v", p, err)
	}
}

func TestSyncRunsAndCursors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	run, err := s.CreateSyncRun(ctx, db.CreateSyncRunParams{ID: uuid.Must(uuid.NewV7()), Provider: "fake", Kind: "dump", Trigger: "manual"})
	if err != nil || run.Status != "running" {
		t.Fatalf("create: %+v %v", run, err)
	}
	if err := s.UpdateSyncRunCounts(ctx, db.UpdateSyncRunCountsParams{ID: run.ID, Upserts: 10, Changed: 4, Notices: []byte(`[]`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishSyncRun(ctx, db.FinishSyncRunParams{ID: run.ID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSyncRun(ctx, run.ID)
	if err != nil || got.Status != "succeeded" || got.FinishedAt == nil || got.Upserts != 10 {
		t.Fatalf("get: %+v %v", got, err)
	}
	// A run left "running" by a crash is failed at startup.
	if _, err := s.CreateSyncRun(ctx, db.CreateSyncRunParams{ID: uuid.Must(uuid.NewV7()), Provider: "fake", Kind: "dump"}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.FailInterruptedSyncRuns(ctx); err != nil || n != 1 {
		t.Fatalf("fail interrupted: %d %v", n, err)
	}
	runs, err := s.ListSyncRuns(ctx, db.ListSyncRunsParams{Provider: pgtype.Text{String: "fake", Valid: true}, Lim: 10})
	if err != nil || len(runs) != 2 {
		t.Fatalf("list: %v %v", runs, err)
	}
	if err := s.UpsertSyncCursor(ctx, db.UpsertSyncCursorParams{Provider: "fake", Kind: "dump", Cursor: "abc", RunID: uuid.NullUUID{UUID: run.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	cur, err := s.GetSyncCursor(ctx, db.GetSyncCursorParams{Provider: "fake", Kind: "dump"})
	if err != nil || cur.Cursor != "abc" {
		t.Fatalf("cursor: %+v %v", cur, err)
	}
}
