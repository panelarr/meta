// SPDX-License-Identifier: GPL-3.0-or-later

package catalog

import (
	"testing"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
)

func rec(provider, id string, f func(e *metadatav1.ProviderEntry)) Source {
	e := &metadatav1.ProviderEntry{Ref: &metadatav1.ExternalId{Provider: provider, Id: id}}
	f(e)
	return Source{Provider: provider, ExternalID: id, State: "active", Record: e}
}

func TestBuildPerFieldPriorityAndLocks(t *testing.T) {
	a := rec("alpha", "1", func(e *metadatav1.ProviderEntry) {
		e.Type = metadatav1.EntryType_ENTRY_TYPE_MANGA
		e.RawType = "Manga"
		e.Titles = []*metadatav1.Title{{Text: "First Light", Language: "en", IsPrimary: true}}
		e.Credits = []*metadatav1.Credit{{Person: &metadatav1.Person{Name: "A. Writer"}}}
		e.ExternalIds = []*metadatav1.ExternalId{{Provider: "anilist", Id: "10"}}
		e.Stats = &metadatav1.Stats{Rating: ptr(80.0)}
	})
	b := rec("beta", "x", func(e *metadatav1.ProviderEntry) {
		e.Type = metadatav1.EntryType_ENTRY_TYPE_MANHWA
		e.Titles = []*metadatav1.Title{
			{Text: "first light", Language: "en"}, // duplicate of alpha's
			{Text: "Hajimari", Language: "ja-Latn", Kind: metadatav1.TitleKind_TITLE_KIND_ROMANIZED},
		}
		e.Credits = []*metadatav1.Credit{{Person: &metadatav1.Person{Name: "Beta Person"}}}
		e.OriginalLanguage = "ja"
	})
	prio := Priority{PerField: map[string][]string{"credits": {"beta"}}, Fallback: []string{"alpha", "beta"}}
	m := Build([]Source{b, a}, prio, nil, "en")

	if m.Entry.GetType() != metadatav1.EntryType_ENTRY_TYPE_MANGA || m.Entry.GetRawType() != "Manga" || m.FieldSources["type"] != "alpha" {
		t.Errorf("type: %v %q %v", m.Entry.GetType(), m.Entry.GetRawType(), m.FieldSources["type"])
	}
	if m.Entry.GetCredits()[0].GetPerson().GetName() != "Beta Person" || m.FieldSources["credits"] != "beta" {
		t.Errorf("credits: %v", m.Entry.GetCredits())
	}
	if m.Entry.GetOriginalLanguage() != "ja" || m.FieldSources["original_language"] != "beta" {
		t.Errorf("language falls through to beta: %q", m.Entry.GetOriginalLanguage())
	}
	if len(m.Entry.GetTitles()) != 2 || m.Title != "First Light" {
		t.Errorf("titles: %v, display %q", m.Entry.GetTitles(), m.Title)
	}
	if len(m.ExternalIDs) != 3 {
		t.Errorf("external ids: %v", m.ExternalIDs)
	}
	if m.Stats["alpha"].GetRating() != 80 || m.Stats["beta"] != nil {
		t.Errorf("stats: %v", m.Stats)
	}

	locks := []Lock{
		{Field: "type", Provider: "beta"},
		{Field: "status", Value: []byte(`{"status":"ENTRY_STATUS_HIATUS","rawStatus":"on break"}`)},
		{Field: "title", Value: []byte(`"Curated Title"`)},
	}
	m = Build([]Source{a, b}, prio, locks, "en")
	if m.Entry.GetType() != metadatav1.EntryType_ENTRY_TYPE_MANHWA || m.Entry.GetRawType() != "" || m.FieldSources["type"] != "beta" {
		t.Errorf("provider lock: %v %q", m.Entry.GetType(), m.Entry.GetRawType())
	}
	if m.Entry.GetStatus() != metadatav1.EntryStatus_ENTRY_STATUS_HIATUS || m.Entry.GetRawStatus() != "on break" || m.FieldSources["status"] != ManualSource {
		t.Errorf("value lock: %v %q", m.Entry.GetStatus(), m.Entry.GetRawStatus())
	}
	if m.Title != "Curated Title" {
		t.Errorf("title lock: %q", m.Title)
	}
}

func TestPickTitle(t *testing.T) {
	titles := []*metadatav1.Title{
		{Text: "Shingeki", Language: "ja-Latn"},
		{Text: "進撃", Language: "ja", IsPrimary: true},
		{Text: "Attack", Language: "en", IsPrimary: true},
	}
	if got := PickTitle(titles, "en"); got != "Attack" {
		t.Errorf("en: %q", got)
	}
	if got := PickTitle(titles, "ja"); got != "進撃" {
		t.Errorf("ja: %q", got)
	}
	if got := PickTitle(titles, "de"); got != "進撃" {
		t.Errorf("de falls back to the first primary: %q", got)
	}
	if got := PickTitle(titles[:1], "en"); got != "Shingeki" {
		t.Errorf("no primary: %q", got)
	}
}

func ptr[T any](v T) *T { return &v }
