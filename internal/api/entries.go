// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/enums"
	"github.com/panelarr/meta/internal/store/db"
)

var unmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}

// decodeList reads a JSON array of protobuf messages.
func decodeList[T proto.Message](b []byte, newT func() T) ([]T, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(b, &raws); err != nil {
		return nil, err
	}
	out := make([]T, 0, len(raws))
	for _, r := range raws {
		v := newT()
		if err := unmarshal.Unmarshal(r, v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// decodeOne reads one protobuf message from JSON; nil or empty input
// gives nil.
func decodeOne[T proto.Message](b []byte, newT func() T) (T, error) {
	var zero T
	if len(b) == 0 || string(b) == "null" {
		return zero, nil
	}
	v := newT()
	if err := unmarshal.Unmarshal(b, v); err != nil {
		return zero, err
	}
	return v, nil
}

func tagProto(id uuid.UUID, key string, path []string, name, description string, level int32, isGenre, isSpoiler, isExplicit bool, rating string, count int64, data []byte) *metadatav1.Tag {
	t := &metadatav1.Tag{}
	_ = unmarshal.Unmarshal(data, t)
	t.Id = id.String()
	t.Key = key
	t.Path = path
	if len(path) > 0 {
		t.Parent = path[len(path)-1]
	}
	t.Name = name
	t.Description = description
	t.Level = level
	t.IsGenre = isGenre
	t.IsSpoiler = isSpoiler
	t.IsExplicit = isExplicit
	t.ContentRating = enums.MustParse[metadatav1.ContentRating](rating)
	t.EntryCount = count
	return t
}

func titleProto(t db.EntryTitle) *metadatav1.Title {
	return &metadatav1.Title{
		Text: t.Text, Language: t.Language, Kind: enums.MustParse[metadatav1.TitleKind](t.Kind),
		IsPrimary: t.IsPrimary, Traits: t.Traits, Note: t.Note,
	}
}

// summaries builds list items for ids, in the same order, with a handful
// of queries for the whole page.
func (m *Meta) summaries(ctx context.Context, ids []uuid.UUID, lang string) ([]*metav1.EntrySummary, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := m.st.GetEntries(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]db.Entry, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	titleRows, err := m.st.ListTitlesForEntries(ctx, ids)
	if err != nil {
		return nil, err
	}
	titles := map[uuid.UUID][]*metadatav1.Title{}
	for _, t := range titleRows {
		titles[t.EntryID] = append(titles[t.EntryID], titleProto(t))
	}
	genreRows, err := m.st.ListGenresForEntries(ctx, ids)
	if err != nil {
		return nil, err
	}
	genres := map[uuid.UUID][]string{}
	for _, g := range genreRows {
		genres[g.EntryID] = append(genres[g.EntryID], g.Name)
	}
	attrRows, err := m.st.ListAttributionsForEntries(ctx, ids)
	if err != nil {
		return nil, err
	}
	attrs := map[uuid.UUID][]*metadatav1.Attribution{}
	for _, a := range attrRows {
		if at, err := decodeOne(a.Attribution, func() *metadatav1.Attribution { return &metadatav1.Attribution{} }); err == nil && at != nil {
			attrs[a.EntryID.UUID] = append(attrs[a.EntryID.UUID], at)
		}
	}
	out := make([]*metav1.EntrySummary, 0, len(ids))
	for _, id := range ids {
		r, ok := byID[id]
		if !ok {
			out = append(out, &metav1.EntrySummary{Id: id.String()})
			continue
		}
		s := &metav1.EntrySummary{
			Id:               id.String(),
			Title:            displayTitle(r.Title, titles[id], lang, m.opts.DisplayLanguage),
			Type:             enums.MustParse[metadatav1.EntryType](r.Type),
			Status:           enums.MustParse[metadatav1.EntryStatus](r.Status),
			ContentRating:    enums.MustParse[metadatav1.ContentRating](r.ContentRating),
			OriginalLanguage: r.OriginalLanguage,
			Year:             r.Year.Int32,
			Genres:           genres[id],
			Attributions:     attrs[id],
		}
		if r.Rating.Valid {
			s.Rating = &r.Rating.Float64
		}
		if cover, err := decodeOne(r.Cover, func() *metadatav1.Image { return &metadatav1.Image{} }); err == nil {
			s.Cover = cover
		}
		out = append(out, s)
	}
	return out, nil
}

// displayTitle uses the stored display title unless the request asks for
// another language.
func displayTitle(stored string, titles []*metadatav1.Title, lang, defaultLang string) string {
	if lang == "" || lang == defaultLang {
		return stored
	}
	if t := catalog.PickTitle(titles, lang); t != "" {
		// Only switch when the entry really has a title in that language.
		for _, ti := range titles {
			if ti.GetText() == t && (ti.GetLanguage() == lang || len(ti.GetLanguage()) > len(lang) && ti.GetLanguage()[:len(lang)+1] == lang+"-") {
				return t
			}
		}
	}
	return stored
}

// loadEntry reads a full entry with its parts.
func (m *Meta) loadEntry(ctx context.Context, id uuid.UUID, lang string) (*metav1.Entry, error) {
	r, err := m.st.GetEntry(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no entry %s", id))
	}
	if err != nil {
		return nil, err
	}
	e := &metav1.Entry{
		Id:               id.String(),
		Type:             enums.MustParse[metadatav1.EntryType](r.Type),
		RawType:          r.RawType,
		Status:           enums.MustParse[metadatav1.EntryStatus](r.Status),
		RawStatus:        r.RawStatus,
		ContentRating:    enums.MustParse[metadatav1.ContentRating](r.ContentRating),
		RawContentRating: r.RawContentRating,
		OriginalLanguage: r.OriginalLanguage,
		State:            enums.MustParse[metadatav1.EntryState](r.State),
		CreatedAt:        timestamppb.New(r.CreatedAt),
		UpdatedAt:        timestamppb.New(r.UpdatedAt),
	}
	if r.MergedInto.Valid {
		e.MergedInto = r.MergedInto.UUID.String()
	}
	if err := json.Unmarshal(r.FieldSources, &e.FieldSources); err != nil {
		return nil, fmt.Errorf("entry %s: field sources: %w", id, err)
	}
	if e.Dates, err = decodeOne(r.Dates, func() *metadatav1.Dates { return &metadatav1.Dates{} }); err != nil {
		return nil, err
	}
	if e.Progress, err = decodeOne(r.Progress, func() *metadatav1.Progress { return &metadatav1.Progress{} }); err != nil {
		return nil, err
	}
	if e.Adaptations, err = decodeList(r.Adaptations, func() *metadatav1.Adaptation { return &metadatav1.Adaptation{} }); err != nil {
		return nil, err
	}
	if e.Items, err = decodeList(r.Items, func() *metadatav1.Item { return &metadatav1.Item{} }); err != nil {
		return nil, err
	}
	if e.Licenses, err = decodeList(r.Licenses, func() *metadatav1.License { return &metadatav1.License{} }); err != nil {
		return nil, err
	}
	if e.Stats, err = decodeList(r.Stats, func() *metav1.ProviderStats { return &metav1.ProviderStats{} }); err != nil {
		return nil, err
	}

	titles, err := m.st.ListEntryTitles(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, t := range titles {
		e.Titles = append(e.Titles, titleProto(t))
	}
	e.Title = displayTitle(r.Title, e.Titles, lang, m.opts.DisplayLanguage)

	descs, err := m.st.ListEntryDescriptions(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, d := range descs {
		e.Descriptions = append(e.Descriptions, &metadatav1.Description{
			Language: d.Language, Text: d.Text, Format: enums.MustParse[metadatav1.DescriptionFormat](d.Format),
		})
	}

	credits, err := m.st.ListEntryCredits(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, c := range credits {
		cr := &metadatav1.Credit{}
		_ = unmarshal.Unmarshal(c.Data, cr)
		p := &metadatav1.Person{}
		_ = unmarshal.Unmarshal(c.PersonData, p)
		p.Id, p.Name = c.PersonID.String(), c.PersonName
		cr.Person = p
		cr.Role = enums.MustParse[metadatav1.PersonRole](c.Role)
		cr.RawRole = c.RawRole
		e.Credits = append(e.Credits, cr)
	}

	companies, err := m.st.ListEntryCompanies(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, c := range companies {
		cc := &metadatav1.CompanyCredit{}
		_ = unmarshal.Unmarshal(c.Data, cc)
		co := &metadatav1.Company{}
		_ = unmarshal.Unmarshal(c.CompanyData, co)
		co.Id, co.Name = c.CompanyID.String(), c.CompanyName
		cc.Company = co
		cc.Role = enums.MustParse[metadatav1.CompanyRole](c.Role)
		cc.RawRole = c.RawRole
		e.Companies = append(e.Companies, cc)
	}

	tags, err := m.st.ListEntryTags(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, t := range tags {
		ta := &metadatav1.TagAssignment{
			Tag:       tagProto(t.TagID, t.TagKey, t.TagPath, t.Name, t.Description, t.Level, t.IsGenre, t.TagIsSpoiler, t.IsExplicit, t.ContentRating, t.EntryCount, t.TagData),
			Weight:    enums.MustParse[metadatav1.TagWeight](t.Weight),
			RawWeight: t.RawWeight,
			IsSpoiler: t.IsSpoiler,
		}
		if t.Score.Valid {
			ta.Score = &t.Score.Float64
		}
		e.Tags = append(e.Tags, ta)
	}

	rels, err := m.st.ListEntryRelations(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, rl := range rels {
		title := rl.Title
		if title == "" {
			title = rl.TargetTitle
		}
		e.Relations = append(e.Relations, &metadatav1.Relation{
			Type: enums.MustParse[metadatav1.RelationType](rl.Type), RawType: rl.RawType,
			Target:  &metadatav1.ExternalId{Provider: rl.TargetProvider, Id: rl.TargetID},
			EntryId: rl.TargetEntryID, Title: title,
			Chronology: enums.MustParse[metadatav1.RelationChronology](rl.Chronology),
			Manual:     rl.Manual, Note: rl.Note,
		})
	}

	images, err := m.st.ListEntryImages(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, im := range images {
		img := &metadatav1.Image{}
		if err := unmarshal.Unmarshal(im.Data, img); err == nil {
			e.Images = append(e.Images, img)
		}
	}
	links, err := m.st.ListEntryLinks(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		ln := &metadatav1.Link{}
		if err := unmarshal.Unmarshal(l.Data, ln); err == nil {
			e.Links = append(e.Links, ln)
		}
	}
	xids, err := m.st.ListEntryExternalIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, x := range xids {
		e.ExternalIds = append(e.ExternalIds, &metadatav1.ExternalId{Provider: x.Provider, Id: x.ExternalID, Url: x.Url})
	}

	sources, err := m.st.ListSourcesForEntry(ctx, uuid.NullUUID{UUID: id, Valid: true})
	if err != nil {
		return nil, err
	}
	for _, s := range sources {
		src := &metav1.EntrySource{
			Ref:       &metadatav1.ExternalId{Provider: s.Provider, Id: s.ExternalID},
			State:     enums.MustParse[metadatav1.EntryState](s.State),
			FetchedAt: timestamppb.New(s.FetchedAt),
		}
		if s.ProviderUpdatedAt != nil {
			src.ProviderUpdatedAt = timestamppb.New(*s.ProviderUpdatedAt)
		}
		if at, err := decodeOne(s.Attribution, func() *metadatav1.Attribution { return &metadatav1.Attribution{} }); err == nil {
			src.Attribution = at
		}
		var rec struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(s.Record, &rec) == nil {
			src.Ref.Url = rec.URL
		}
		if src.Attribution == nil {
			if p, ok := m.host.Get(s.Provider); ok {
				src.Attribution = p.Info().GetAttribution()
			}
		}
		e.Sources = append(e.Sources, src)
	}
	return e, nil
}
