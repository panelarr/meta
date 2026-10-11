// SPDX-License-Identifier: GPL-3.0-or-later

package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/enums"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/internal/textnorm"
)

func (t *tx) clearParts(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	for _, del := range []func(context.Context, []uuid.UUID) error{
		t.q.DeleteTitlesOf, t.q.DeleteDescriptionsOf, t.q.DeleteCreditsOf, t.q.DeleteCompaniesOf,
		t.q.DeleteTagsOf, t.q.DeleteRelationsOf, t.q.DeleteImagesOf, t.q.DeleteLinksOf, t.q.DeleteExternalIDsOf,
	} {
		if err := del(ctx, ids); err != nil {
			return err
		}
	}
	return nil
}

// flushDirty rebuilds every entry touched in the transaction, as a set.
func (t *tx) flushDirty(ctx context.Context) error {
	if len(t.dirty) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(t.dirty))
	for id := range t.dirty {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	t.dirty = map[uuid.UUID]bool{}

	entries, err := t.q.GetEntries(ctx, ids)
	if err != nil {
		return err
	}
	cur := make(map[uuid.UUID]db.Entry, len(entries))
	for _, e := range entries {
		cur[e.ID] = e
	}
	recs, err := t.q.ListProviderEntriesForEntries(ctx, ids)
	if err != nil {
		return err
	}
	sources := map[uuid.UUID][]Source{}
	for _, r := range recs {
		var e metadatav1.ProviderEntry
		if err := protojson.Unmarshal(r.Record, &e); err != nil {
			return fmt.Errorf("catalog: decode %s:%s: %w", r.Provider, r.ExternalID, err)
		}
		sources[r.EntryID.UUID] = append(sources[r.EntryID.UUID], Source{Provider: r.Provider, ExternalID: r.ExternalID, State: r.State, Record: &e})
	}
	lockRows, err := t.q.ListFieldLocksForEntries(ctx, ids)
	if err != nil {
		return err
	}
	locks := map[uuid.UUID][]Lock{}
	for _, l := range lockRows {
		locks[l.EntryID] = append(locks[l.EntryID], Lock{Field: l.Field, Provider: l.Provider.String, Value: l.Value})
	}

	var (
		updates []db.UpdateEntryBatchParams
		changed []uuid.UUID
		merged  = map[uuid.UUID]*Merged{}
	)
	for _, id := range ids {
		c, ok := cur[id]
		if !ok || c.State == "merged" {
			continue
		}
		var active []Source
		for _, s := range sources[id] {
			if s.State == "active" {
				active = append(active, s)
			}
		}
		if len(active) == 0 {
			// Keep what was there, so links and details still work.
			if c.State != "removed" {
				if err := t.q.SetEntryState(ctx, db.SetEntryStateParams{ID: id, State: "removed"}); err != nil {
					return err
				}
				t.result.EntriesChanged++
			}
			continue
		}
		m := Build(active, t.w.opts.Priority, locks[id], t.w.opts.DisplayLanguage)
		m.ExternalIDs = externalIDs(sources[id])
		row, err := t.entryRow(id, m)
		if err != nil {
			return err
		}
		if !t.force && c.State == "active" && bytes.Equal(c.ContentHash, row.ContentHash) {
			continue
		}
		updates = append(updates, row)
		changed = append(changed, id)
		merged[id] = m
	}
	if len(updates) == 0 {
		return nil
	}
	if err := batchExec(len(updates), func(f func(int, error)) {
		t.q.UpdateEntryBatch(ctx, updates).Exec(f)
	}); err != nil {
		return err
	}
	var stale []uuid.UUID
	for _, id := range changed {
		if !t.fresh[id] {
			stale = append(stale, id)
		}
	}
	if err := t.clearParts(ctx, stale); err != nil {
		return err
	}
	if err := t.prefetchPersons(ctx, changed, merged); err != nil {
		return err
	}
	var p parts
	for _, id := range changed {
		if err := t.collectParts(ctx, &p, id, merged[id]); err != nil {
			return fmt.Errorf("catalog: entry %s: %w", id, err)
		}
	}
	if err := p.copy(ctx, t.q); err != nil {
		return err
	}
	t.result.EntriesChanged += len(changed)
	return nil
}

func marshalList[T proto.Message](items []T) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		j, err := pj.Marshal(it)
		if err != nil {
			return nil, err
		}
		b.Write(j)
	}
	b.WriteByte(']')
	return b.Bytes(), nil
}

func marshalOpt(m proto.Message) ([]byte, error) {
	if m == nil || !m.ProtoReflect().IsValid() {
		return nil, nil
	}
	return pj.Marshal(m)
}

// entryRow computes the entries row for a merged entry, including the
// content hash that lets unchanged rebuilds write nothing.
func (t *tx) entryRow(id uuid.UUID, m *Merged) (db.UpdateEntryBatchParams, error) {
	e := m.Entry
	row := db.UpdateEntryBatchParams{
		ID:               id,
		State:            "active",
		Title:            m.Title,
		Type:             enums.Name(e.GetType()),
		RawType:          e.GetRawType(),
		Status:           enums.Name(e.GetStatus()),
		RawStatus:        e.GetRawStatus(),
		ContentRating:    enums.Name(e.GetContentRating()),
		RawContentRating: e.GetRawContentRating(),
		OriginalLanguage: e.GetOriginalLanguage(),
	}
	if y := e.GetDates().GetStart().GetYear(); y > 0 {
		row.Year = pgtype.Int4{Int32: y, Valid: true}
	}
	// Sort keys come from the most preferred provider that has them.
	providers := make([]string, 0, len(m.Stats))
	for p := range m.Stats {
		providers = append(providers, p)
	}
	statsOrder := t.w.opts.Priority.Order("stats", providers)
	for _, p := range statsOrder {
		s := m.Stats[p]
		if !row.Rating.Valid && s.Rating != nil {
			row.Rating = pgtype.Float8{Float64: s.GetRating(), Valid: true}
		}
		if !row.Popularity.Valid && s.Popularity != nil {
			row.Popularity = pgtype.Float8{Float64: s.GetPopularity(), Valid: true}
		}
		if !row.PopularityRank.Valid && s.PopularityRank != nil {
			row.PopularityRank = pgtype.Int8{Int64: s.GetPopularityRank(), Valid: true}
		}
	}
	var err error
	for _, img := range e.GetImages() {
		if img.GetKind() == metadatav1.ImageKind_IMAGE_KIND_COVER {
			if row.Cover, err = pj.Marshal(img); err != nil {
				return row, err
			}
			break
		}
	}
	if row.Dates, err = marshalOpt(e.GetDates()); err != nil {
		return row, err
	}
	if row.Progress, err = marshalOpt(e.GetProgress()); err != nil {
		return row, err
	}
	if row.Adaptations, err = marshalList(e.GetAdaptations()); err != nil {
		return row, err
	}
	if row.Items, err = marshalList(e.GetItems()); err != nil {
		return row, err
	}
	if row.Licenses, err = marshalList(e.GetLicenses()); err != nil {
		return row, err
	}
	stats := make([]*metav1.ProviderStats, 0, len(statsOrder))
	for _, p := range statsOrder {
		stats = append(stats, &metav1.ProviderStats{Provider: p, Stats: m.Stats[p]})
	}
	if row.Stats, err = marshalList(stats); err != nil {
		return row, err
	}
	// encoding/json sorts map keys, which keeps the hash stable.
	if row.FieldSources, err = json.Marshal(m.FieldSources); err != nil {
		return row, err
	}

	words := make([]string, 0, len(e.GetTitles())+len(e.GetCredits()))
	for _, ti := range e.GetTitles() {
		words = append(words, textnorm.Key(ti.GetText()))
	}
	for _, c := range e.GetCredits() {
		words = append(words, textnorm.Key(c.GetPerson().GetName()))
	}
	row.SearchText = strings.Join(words, " ")

	h := sha256.New()
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(e)
	if err != nil {
		return row, err
	}
	h.Write(b)
	h.Write([]byte(m.Title))
	h.Write(row.FieldSources)
	h.Write(row.Stats)
	for _, x := range m.ExternalIDs {
		fmt.Fprintf(h, "\x00%s\x00%s\x00%s\x00%s", x.Provider, x.ID, x.URL, x.Source)
	}
	row.ContentHash = h.Sum(nil)
	return row, nil
}

// parts collects the part rows of many entries for one COPY per table.
type parts struct {
	titles    []db.InsertEntryTitlesParams
	descs     []db.InsertEntryDescriptionsParams
	credits   []db.InsertEntryCreditsParams
	companies []db.InsertEntryCompaniesParams
	tags      []db.InsertEntryTagsParams
	rels      []db.InsertEntryRelationsParams
	images    []db.InsertEntryImagesParams
	links     []db.InsertEntryLinksParams
	xids      []db.InsertEntryExternalIDsParams
}

func (p *parts) copy(ctx context.Context, q *db.Queries) error {
	steps := []func() error{
		func() error { return copyRows(ctx, p.titles, q.InsertEntryTitles) },
		func() error { return copyRows(ctx, p.descs, q.InsertEntryDescriptions) },
		func() error { return copyRows(ctx, p.credits, q.InsertEntryCredits) },
		func() error { return copyRows(ctx, p.companies, q.InsertEntryCompanies) },
		func() error { return copyRows(ctx, p.tags, q.InsertEntryTags) },
		func() error { return copyRows(ctx, p.rels, q.InsertEntryRelations) },
		func() error { return copyRows(ctx, p.images, q.InsertEntryImages) },
		func() error { return copyRows(ctx, p.links, q.InsertEntryLinks) },
		func() error { return copyRows(ctx, p.xids, q.InsertEntryExternalIDs) },
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return err
		}
	}
	return nil
}

func copyRows[T any](ctx context.Context, rows []T, f func(context.Context, []T) (int64, error)) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := f(ctx, rows)
	return err
}

func (t *tx) collectParts(ctx context.Context, p *parts, id uuid.UUID, m *Merged) error {
	e := m.Entry
	src := func(field string) string { return m.FieldSources[field] }

	for i, ti := range e.GetTitles() {
		traits := ti.GetTraits()
		if traits == nil {
			traits = []string{}
		}
		p.titles = append(p.titles, db.InsertEntryTitlesParams{
			EntryID: id, Position: int32(i), Text: ti.GetText(), Norm: textnorm.Key(ti.GetText()),
			Language: ti.GetLanguage(), Kind: enums.Name(ti.GetKind()), IsPrimary: ti.GetIsPrimary(),
			Traits: traits, Note: ti.GetNote(), Source: src("titles"),
		})
	}
	for i, d := range e.GetDescriptions() {
		p.descs = append(p.descs, db.InsertEntryDescriptionsParams{
			EntryID: id, Position: int32(i), Language: d.GetLanguage(), Text: d.GetText(),
			Format: enums.Name(d.GetFormat()), Source: src("descriptions"),
		})
	}
	for i, c := range e.GetCredits() {
		if c.GetPerson().GetName() == "" {
			continue
		}
		pid, err := t.person(ctx, c.GetPerson())
		if err != nil {
			return err
		}
		rest := proto.Clone(c).(*metadatav1.Credit)
		rest.Person, rest.Role, rest.RawRole = nil, 0, ""
		data, err := pj.Marshal(rest)
		if err != nil {
			return err
		}
		p.credits = append(p.credits, db.InsertEntryCreditsParams{
			EntryID: id, Position: int32(i), PersonID: pid, Role: enums.Name(c.GetRole()),
			RawRole: c.GetRawRole(), Data: data, Source: src("credits"),
		})
	}
	for i, c := range e.GetCompanies() {
		if c.GetCompany().GetName() == "" {
			continue
		}
		cid, err := t.company(ctx, c.GetCompany())
		if err != nil {
			return err
		}
		rest := proto.Clone(c).(*metadatav1.CompanyCredit)
		rest.Company, rest.Role, rest.RawRole = nil, 0, ""
		data, err := pj.Marshal(rest)
		if err != nil {
			return err
		}
		p.companies = append(p.companies, db.InsertEntryCompaniesParams{
			EntryID: id, Position: int32(i), CompanyID: cid, Role: enums.Name(c.GetRole()),
			RawRole: c.GetRawRole(), Data: data, Source: src("companies"),
		})
	}
	seenTag := map[uuid.UUID]bool{}
	for i, ta := range e.GetTags() {
		if ta.GetTag().GetName() == "" {
			continue
		}
		tid, err := t.tag(ctx, ta.GetTag())
		if err != nil {
			return err
		}
		if seenTag[tid] {
			continue
		}
		seenTag[tid] = true
		row := db.InsertEntryTagsParams{
			EntryID: id, TagID: tid, Position: int32(i), Weight: enums.Name(ta.GetWeight()),
			RawWeight: ta.GetRawWeight(), IsSpoiler: ta.GetIsSpoiler(), Source: src("tags"),
		}
		if ta.Score != nil {
			row.Score = pgtype.Float8{Float64: ta.GetScore(), Valid: true}
		}
		p.tags = append(p.tags, row)
	}
	for i, r := range e.GetRelations() {
		if r.GetTarget().GetId() == "" {
			continue
		}
		p.rels = append(p.rels, db.InsertEntryRelationsParams{
			EntryID: id, Position: int32(i), Type: enums.Name(r.GetType()), RawType: r.GetRawType(),
			TargetProvider: r.GetTarget().GetProvider(), TargetID: r.GetTarget().GetId(), Title: r.GetTitle(),
			Chronology: enums.Name(r.GetChronology()), Manual: r.GetManual(), Note: r.GetNote(), Source: src("relations"),
		})
	}
	for i, img := range e.GetImages() {
		data, err := pj.Marshal(img)
		if err != nil {
			return err
		}
		p.images = append(p.images, db.InsertEntryImagesParams{
			EntryID: id, Position: int32(i), Kind: enums.Name(img.GetKind()), Url: img.GetUrl(), Data: data, Source: src("images"),
		})
	}
	for i, l := range e.GetLinks() {
		if l.GetUrl() == "" {
			continue
		}
		data, err := pj.Marshal(l)
		if err != nil {
			return err
		}
		p.links = append(p.links, db.InsertEntryLinksParams{
			EntryID: id, Position: int32(i), Type: enums.Name(l.GetType()), Url: l.GetUrl(), Data: data, Source: src("links"),
		})
	}
	for _, x := range m.ExternalIDs {
		p.xids = append(p.xids, db.InsertEntryExternalIDsParams{Provider: x.Provider, ExternalID: x.ID, EntryID: id, Url: x.URL, Source: x.Source})
	}
	return nil
}
