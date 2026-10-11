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
)

func genre(name string) *metadatav1.TagAssignment {
	return &metadatav1.TagAssignment{Tag: &metadatav1.Tag{Name: name, IsGenre: true}}
}

func tagUnder(name string, path ...string) *metadatav1.TagAssignment {
	return &metadatav1.TagAssignment{Tag: &metadatav1.Tag{Name: name, Path: path, Parent: path[len(path)-1]}}
}

func genresOf(t *testing.T, s *store.Store, id uuid.UUID) []string {
	t.Helper()
	rows, err := s.ListGenreKeysForEntries(context.Background(), []uuid.UUID{id})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// A "Settings > Fantasy" tag must not make Fantasy a genre of the entry,
// even when another entry has the genre Fantasy.
func TestTagWithAGenresNameStaysATag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, s := newWriter(t)
	punpun := record("alpha", "1", "A Quiet Bird")
	punpun.Tags = []*metadatav1.TagAssignment{
		genre("Drama"), genre("Psychological"),
		tagUnder("Fantasy", "Settings"), tagUnder("Supernatural", "Settings"), tagUnder("Comedy", "Themes"),
	}
	elf := record("alpha", "2", "Elf Journey")
	elf.Tags = []*metadatav1.TagAssignment{genre("Fantasy"), genre("Comedy")}
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{punpun, elf}}); err != nil {
		t.Fatal(err)
	}
	a, b := entryOf(t, s, "alpha", "1"), entryOf(t, s, "alpha", "2")
	if got := genresOf(t, s, a.ID); len(got) != 2 || got[0] != "Drama" || got[1] != "Psychological" {
		t.Fatalf("genres of the first entry, in order: %v", got)
	}
	if got := genresOf(t, s, b.ID); len(got) != 2 || got[0] != "Fantasy" {
		t.Fatalf("genres of the second entry: %v", got)
	}
	if err := w.RecountTags(ctx); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	tags, err := s.ListTagsWithParent(ctx, db.ListTagsWithParentParams{Lim: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range tags {
		keys[tg.Key] = true
	}
	for _, k := range []string{"genre/fantasy", "tag/settings/fantasy", "genre/comedy", "tag/themes/comedy"} {
		if !keys[k] {
			t.Errorf("missing tag %s in %v", k, keys)
		}
	}
}

// RebuildAll fixes entries that carry a wrong tag from before.
func TestRebuildAllRepairsTags(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, s := newWriter(t)
	rec := record("alpha", "1", "A Quiet Bird")
	rec.Tags = []*metadatav1.TagAssignment{genre("Drama"), tagUnder("Fantasy", "Settings")}
	other := record("alpha", "2", "Elf Journey")
	other.Tags = []*metadatav1.TagAssignment{genre("Fantasy")}
	if _, err := w.Apply(ctx, catalog.Batch{Provider: "alpha", Upserts: []*metadatav1.ProviderEntry{rec, other}}); err != nil {
		t.Fatal(err)
	}
	e := entryOf(t, s, "alpha", "1")
	// Simulate the old bug: the entry points at the genre instead of the tag.
	if _, err := s.Pool.Exec(ctx, `UPDATE entry_tags SET tag_id = (SELECT id FROM tags WHERE key = 'genre/fantasy')
		WHERE entry_id = $1 AND tag_id = (SELECT id FROM tags WHERE key = 'tag/settings/fantasy')`, e.ID); err != nil {
		t.Fatal(err)
	}
	if got := genresOf(t, s, e.ID); len(got) != 2 {
		t.Fatalf("setup: %v", got)
	}
	n, err := w.RebuildAll(ctx, 1, nil)
	if err != nil || n != 2 {
		t.Fatalf("rebuild: %d %v", n, err)
	}
	if got := genresOf(t, s, e.ID); len(got) != 1 || got[0] != "Drama" {
		t.Fatalf("after rebuild: %v", got)
	}
}

// The upgrade queues a rebuild; it runs once and is then done.
