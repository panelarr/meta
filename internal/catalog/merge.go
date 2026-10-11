// SPDX-License-Identifier: GPL-3.0-or-later

// Package catalog turns provider records into meta's merged entries and
// writes them: linking records that share external IDs, building each
// entry field from the preferred provider, honouring curator locks, and
// retiring entries that a provider merged.
package catalog

import (
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/textnorm"
)

// ManualSource marks fields a curator set in field_sources.
const ManualSource = "manual"

// Source is one provider record behind an entry.
type Source struct {
	Provider   string
	ExternalID string
	// State of the record: active, merged or removed.
	State  string
	Record *metadatav1.ProviderEntry
}

// Lock pins one field of an entry: to a provider's value, or to a fixed
// value given as a partial ProviderEntry in protobuf JSON
// (e.g. {"type": "ENTRY_TYPE_MANHWA"}). The field "title" pins the display
// title to a JSON string.
type Lock struct {
	Field    string
	Provider string
	Value    []byte
}

// Priority decides which provider's value wins for each field.
type Priority struct {
	// PerField lists providers by preference for a field; "*" applies to
	// fields without their own list.
	PerField map[string][]string
	// Fallback is the order for providers not listed (configuration order).
	Fallback []string
}

// Order returns providers sorted by preference for field.
func (p Priority) Order(field string, providers []string) []string {
	pref := p.PerField[field]
	if pref == nil {
		pref = p.PerField["*"]
	}
	rank := func(pv string) int {
		if i := slices.Index(pref, pv); i >= 0 {
			return i
		}
		if i := slices.Index(p.Fallback, pv); i >= 0 {
			return len(pref) + i
		}
		return len(pref) + len(p.Fallback)
	}
	out := slices.Clone(providers)
	slices.SortStableFunc(out, func(a, b string) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra - rb
		}
		return strings.Compare(a, b)
	})
	return out
}

// Merged is a built entry.
type Merged struct {
	// Entry holds the merged fields. Ref, state and provenance fields are
	// not set.
	Entry *metadatav1.ProviderEntry
	// Title is the display title.
	Title string
	// FieldSources maps field names to the provider they came from.
	FieldSources map[string]string
	// Stats are every source's stats, by provider.
	Stats map[string]*metadatav1.Stats
	// ExternalIDs are every ID that points at the entry.
	ExternalIDs []ExternalID
}

// ExternalID is an ID pointing at an entry, and which record reported it.
type ExternalID struct {
	Provider, ID, URL, Source string
}

// mergedFields are the ProviderEntry fields picked per provider. Each enum
// carries its raw_* companion along.
var mergedFields = []string{
	"type", "status", "content_rating", "titles", "descriptions", "credits", "companies",
	"licenses", "tags", "relations", "adaptations", "items", "progress", "dates",
	"original_language", "images", "links",
}

var rawCompanion = map[string]string{
	"type":           "raw_type",
	"status":         "raw_status",
	"content_rating": "raw_content_rating",
}

var entryDesc = (&metadatav1.ProviderEntry{}).ProtoReflect().Descriptor()

func fieldDesc(name string) protoreflect.FieldDescriptor {
	return entryDesc.Fields().ByName(protoreflect.Name(name))
}

// IsMergedField reports whether name is a field that locks can pin.
func IsMergedField(name string) bool {
	return name == "title" || slices.Contains(mergedFields, name)
}

func copyField(dst, src *metadatav1.ProviderEntry, name string) {
	fd := fieldDesc(name)
	d, s := dst.ProtoReflect(), src.ProtoReflect()
	if s.Has(fd) {
		d.Set(fd, s.Get(fd))
	} else {
		d.Clear(fd)
	}
	if raw, ok := rawCompanion[name]; ok {
		rfd := fieldDesc(raw)
		if s.Has(rfd) {
			d.Set(rfd, s.Get(rfd))
		} else {
			d.Clear(rfd)
		}
	}
}

func has(e *metadatav1.ProviderEntry, name string) bool {
	return e.ProtoReflect().Has(fieldDesc(name))
}

// Build merges active sources into one entry. Sources should be active
// records only; their order does not matter.
func Build(sources []Source, prio Priority, locks []Lock, displayLanguage string) *Merged {
	byProvider := map[string]*metadatav1.ProviderEntry{}
	var providers []string
	// Further records of a provider already seen: only a curator puts two
	// records of one provider on an entry (a confirmed duplicate).
	var extra []*metadatav1.ProviderEntry
	for _, s := range sources {
		if _, dup := byProvider[s.Provider]; dup {
			// The first record of a provider gives its fields; the others
			// add their titles.
			extra = append(extra, s.Record)
			continue
		}
		byProvider[s.Provider] = s.Record
		providers = append(providers, s.Provider)
	}
	slices.Sort(providers)

	lockByField := map[string]Lock{}
	for _, l := range locks {
		lockByField[l.Field] = l
	}

	m := &Merged{
		Entry:        &metadatav1.ProviderEntry{},
		FieldSources: map[string]string{},
		Stats:        map[string]*metadatav1.Stats{},
	}
	for _, field := range mergedFields {
		if l, ok := lockByField[field]; ok {
			if applyLock(m, field, l, byProvider) {
				continue
			}
		}
		order := prio.Order(field, providers)
		if field == "titles" {
			mergeTitles(m, order, byProvider)
			addTitles(m, extra)
			continue
		}
		for _, p := range order {
			if has(byProvider[p], field) {
				copyField(m.Entry, byProvider[p], field)
				m.FieldSources[field] = p
				break
			}
		}
	}

	for _, p := range providers {
		rec := byProvider[p]
		if rec.GetStats() != nil {
			m.Stats[p] = rec.GetStats()
		}
	}
	m.ExternalIDs = externalIDs(sources)
	m.Title = displayTitle(m.Entry.GetTitles(), displayLanguage)
	if l, ok := lockByField["title"]; ok && l.Value != nil {
		if t := strings.Trim(string(l.Value), `" `); t != "" {
			m.Title = t
			m.FieldSources["title"] = ManualSource
		}
	}
	return m
}

func applyLock(m *Merged, field string, l Lock, byProvider map[string]*metadatav1.ProviderEntry) bool {
	if l.Provider != "" {
		rec, ok := byProvider[l.Provider]
		if !ok || !has(rec, field) {
			return false // the pinned provider has nothing: fall back to priority
		}
		copyField(m.Entry, rec, field)
		m.FieldSources[field] = l.Provider
		return true
	}
	var tmp metadatav1.ProviderEntry
	if err := protojson.Unmarshal(l.Value, &tmp); err != nil {
		return false
	}
	copyField(m.Entry, &tmp, field)
	m.FieldSources[field] = ManualSource
	return true
}

// mergeTitles keeps every title from every source, preferred provider first,
// without duplicates of the same text in the same language.
func mergeTitles(m *Merged, order []string, byProvider map[string]*metadatav1.ProviderEntry) {
	seen := map[string]bool{}
	for _, p := range order {
		for _, t := range byProvider[p].GetTitles() {
			key := textnorm.Key(t.GetText()) + "\x00" + t.GetLanguage()
			if t.GetText() == "" || seen[key] {
				continue
			}
			seen[key] = true
			m.Entry.Titles = append(m.Entry.Titles, proto.Clone(t).(*metadatav1.Title))
			if _, ok := m.FieldSources["titles"]; !ok {
				m.FieldSources["titles"] = p
			}
		}
	}
}

// addTitles adds the titles of further records of a provider that the
// merged titles don't have yet.
func addTitles(m *Merged, recs []*metadatav1.ProviderEntry) {
	seen := map[string]bool{}
	for _, t := range m.Entry.GetTitles() {
		seen[textnorm.Key(t.GetText())+"\x00"+t.GetLanguage()] = true
	}
	for _, r := range recs {
		for _, t := range r.GetTitles() {
			key := textnorm.Key(t.GetText()) + "\x00" + t.GetLanguage()
			if t.GetText() == "" || seen[key] {
				continue
			}
			seen[key] = true
			c := proto.Clone(t).(*metadatav1.Title)
			c.IsPrimary = false
			m.Entry.Titles = append(m.Entry.Titles, c)
		}
	}
}

func externalIDs(sources []Source) []ExternalID {
	seen := map[[2]string]bool{}
	var out []ExternalID
	add := func(x ExternalID) {
		k := [2]string{x.Provider, x.ID}
		if x.Provider == "" || x.ID == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, x)
	}
	// Own IDs of every record, active or not, so old IDs keep resolving.
	for _, s := range sources {
		add(ExternalID{Provider: s.Provider, ID: s.ExternalID, URL: s.Record.GetUrl(), Source: s.Provider + ":" + s.ExternalID})
	}
	for _, s := range sources {
		if s.State != "active" {
			continue
		}
		for _, x := range s.Record.GetExternalIds() {
			add(ExternalID{Provider: x.GetProvider(), ID: x.GetId(), URL: x.GetUrl(), Source: s.Provider + ":" + s.ExternalID})
		}
	}
	return out
}

// displayTitle picks the title to show: the primary title in the display
// language, then any primary title, then the first title.
func displayTitle(titles []*metadatav1.Title, lang string) string {
	return PickTitle(titles, lang)
}

// PickTitle chooses a display title for a language preference.
func PickTitle(titles []*metadatav1.Title, lang string) string {
	matchLang := func(t *metadatav1.Title) bool {
		return lang != "" && (t.GetLanguage() == lang || strings.HasPrefix(t.GetLanguage(), lang+"-"))
	}
	for _, t := range titles {
		if t.GetIsPrimary() && matchLang(t) {
			return t.GetText()
		}
	}
	for _, t := range titles {
		if t.GetIsPrimary() && t.GetLanguage() == "" {
			return t.GetText()
		}
	}
	for _, t := range titles {
		if t.GetIsPrimary() {
			return t.GetText()
		}
	}
	for _, t := range titles {
		if matchLang(t) && t.GetKind() == metadatav1.TitleKind_TITLE_KIND_OFFICIAL {
			return t.GetText()
		}
	}
	if len(titles) > 0 {
		return titles[0].GetText()
	}
	return ""
}
