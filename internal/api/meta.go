// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/enums"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/search"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/internal/textnorm"
	"github.com/panelarr/meta/sdk/plugin"
)

// MetaOptions configure the catalogue service.
type MetaOptions struct {
	// DisplayLanguage is the default language for display titles.
	DisplayLanguage string
	// StaleAfter is the age after which reading an entry refreshes it in
	// the background from providers that support lookups. Zero disables it.
	StaleAfter time.Duration
	// Index finds entries. Defaults to the PostgreSQL backend.
	Index search.Index
	Log   *slog.Logger
	// Base is the context background refreshes run in.
	Base context.Context
}

// Meta implements MetaService: browse, search and details.
type Meta struct {
	metav1connect.UnimplementedMetaServiceHandler
	st     *store.Store
	host   *host.Host
	writer *catalog.Writer
	idx    search.Index
	opts   MetaOptions

	refreshing sync.Map // entry ID -> struct{}
	refreshSem chan struct{}
}

// NewMeta returns the catalogue service.
func NewMeta(st *store.Store, h *host.Host, w *catalog.Writer, opts MetaOptions) *Meta {
	if opts.Index == nil {
		opts.Index = search.NewPostgres(st.Pool)
	}
	if opts.Base == nil {
		opts.Base = context.Background()
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &Meta{st: st, host: h, writer: w, idx: opts.Index, opts: opts, refreshSem: make(chan struct{}, 4)}
}

func (m *Meta) lang(asked string) string {
	if asked != "" {
		return asked
	}
	return m.opts.DisplayLanguage
}

func enumNames[T interface{ ~int32 }](vals []T, name func(T) string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v != 0 {
			out = append(out, name(v))
		}
	}
	return out
}

func toFilter(f *metav1.EntryFilter) search.Filter {
	return search.Filter{
		Genres:          f.GetGenres(),
		Tags:            f.GetTags(),
		ExcludeTags:     f.GetExcludeTags(),
		Types:           enumNames(f.GetTypes(), func(v metadatav1.EntryType) string { return enums.Name(v) }),
		Statuses:        enumNames(f.GetStatuses(), func(v metadatav1.EntryStatus) string { return enums.Name(v) }),
		ContentRatings:  enumNames(f.GetContentRatings(), func(v metadatav1.ContentRating) string { return enums.Name(v) }),
		Languages:       f.GetLanguages(),
		YearFrom:        int(f.GetYearFrom()),
		YearTo:          int(f.GetYearTo()),
		TitleLanguages:  f.GetTitleLanguages(),
		IncludeInactive: f.GetIncludeInactive(),
	}
}

func toSort(s metav1.SortField) search.Sort {
	switch s {
	case metav1.SortField_SORT_FIELD_POPULARITY:
		return search.SortPopularity
	case metav1.SortField_SORT_FIELD_RATING:
		return search.SortRating
	case metav1.SortField_SORT_FIELD_UPDATED:
		return search.SortUpdated
	case metav1.SortField_SORT_FIELD_YEAR:
		return search.SortYear
	case metav1.SortField_SORT_FIELD_TITLE:
		return search.SortTitle
	case metav1.SortField_SORT_FIELD_RELEVANCE:
		return search.SortRelevance
	default:
		return search.SortDefault
	}
}

func (m *Meta) Browse(ctx context.Context, req *connect.Request[metav1.BrowseRequest]) (*connect.Response[metav1.BrowseResponse], error) {
	r := req.Msg
	size := pageSize(r.GetPageSize(), 24, 100)
	off, err := decodeOffset(r.GetPageToken())
	if err != nil {
		return nil, err
	}
	res, err := m.idx.Query(ctx, search.Query{
		Filter: toFilter(r.GetFilter()), Sort: toSort(r.GetSort()), Reverse: r.GetReverse(), Limit: size, Offset: off,
	})
	if err != nil {
		return nil, err
	}
	sums, err := m.summaries(ctx, hitIDs(res.Hits), m.lang(r.GetLanguage()))
	if err != nil {
		return nil, err
	}
	out := &metav1.BrowseResponse{Entries: sums, TotalSize: res.Total, NoProviders: m.host.Empty()}
	if int64(off+size) < res.Total {
		out.NextPageToken = encodeOffset(off + size)
	}
	return connect.NewResponse(out), nil
}

func (m *Meta) Search(ctx context.Context, req *connect.Request[metav1.SearchRequest]) (*connect.Response[metav1.SearchResponse], error) {
	r := req.Msg
	if strings.TrimSpace(r.GetQuery()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query is empty; use Browse to list entries"))
	}
	if len(r.GetQuery()) > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query is longer than 500 characters"))
	}
	size := pageSize(r.GetPageSize(), 24, 100)
	off, err := decodeOffset(r.GetPageToken())
	if err != nil {
		return nil, err
	}
	res, err := m.idx.Query(ctx, search.Query{
		Text: r.GetQuery(), Filter: toFilter(r.GetFilter()), Sort: toSort(r.GetSort()), Reverse: r.GetReverse(), Limit: size, Offset: off,
	})
	if err != nil {
		return nil, err
	}
	sums, err := m.summaries(ctx, hitIDs(res.Hits), m.lang(r.GetLanguage()))
	if err != nil {
		return nil, err
	}
	out := &metav1.SearchResponse{TotalSize: res.Total, NoProviders: m.host.Empty()}
	for i, h := range res.Hits {
		out.Hits = append(out.Hits, &metav1.SearchHit{Entry: sums[i], Score: h.Score, Matched: h.Matched, MatchedField: h.Field})
	}
	if int64(off+size) < res.Total {
		out.NextPageToken = encodeOffset(off + size)
	}
	return connect.NewResponse(out), nil
}

func hitIDs(hits []search.Hit) []uuid.UUID {
	ids := make([]uuid.UUID, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return ids
}

func (m *Meta) GetEntry(ctx context.Context, req *connect.Request[metav1.GetEntryRequest]) (*connect.Response[metav1.GetEntryResponse], error) {
	id, err := resolveEntryID(ctx, m.st, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	e, err := m.loadEntry(ctx, id, m.lang(req.Msg.GetLanguage()))
	if err != nil {
		return nil, err
	}
	out := &metav1.GetEntryResponse{Entry: e}
	if id.String() != req.Msg.GetId() {
		out.RedirectedFrom = req.Msg.GetId()
	}
	m.refreshIfStale(e)
	return connect.NewResponse(out), nil
}

func (m *Meta) LookupExternal(ctx context.Context, req *connect.Request[metav1.LookupExternalRequest]) (*connect.Response[metav1.LookupExternalResponse], error) {
	provider, extID := strings.ToLower(strings.TrimSpace(req.Msg.GetProvider())), strings.TrimSpace(req.Msg.GetExternalId())
	if provider == "" || extID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("provider and external_id are required"))
	}
	lang := m.lang(req.Msg.GetLanguage())
	if !req.Msg.GetRefresh() {
		if id, ok, err := m.findExternal(ctx, provider, extID); err != nil {
			return nil, err
		} else if ok {
			e, err := m.loadEntry(ctx, id, lang)
			if err != nil {
				return nil, err
			}
			m.refreshIfStale(e)
			return connect.NewResponse(&metav1.LookupExternalResponse{Entry: e}), nil
		}
	}
	// Ask plugins: the provider itself first, then any plugin that can
	// resolve other providers' IDs.
	ref := &metadatav1.ExternalId{Provider: provider, Id: extID}
	var lastErr error
	for _, p := range m.lookupOrder(ctx, provider) {
		rec, err := p.Get(ctx, ref)
		if err != nil {
			if plugin.ErrorCode(err) != plugin.CodeNotFound {
				lastErr = err
			}
			continue
		}
		b := catalog.Batch{Provider: p.ID(), Upserts: []*metadatav1.ProviderEntry{rec}}
		if rec.GetState() == metadatav1.EntryState_ENTRY_STATE_MERGED && rec.GetMergedInto().GetId() != "" {
			b.Merges = []catalog.Merge{{Merged: rec.GetRef().GetId(), Into: rec.GetMergedInto().GetId()}}
		}
		if _, err := m.writer.Apply(ctx, b); err != nil {
			return nil, err
		}
		id, ok, err := m.findExternal(ctx, provider, extID)
		if err != nil {
			return nil, err
		}
		if !ok {
			// The plugin returned its own record for a foreign ID that the
			// record doesn't list; resolve through the record instead.
			id, ok, err = m.findExternal(ctx, rec.GetRef().GetProvider(), rec.GetRef().GetId())
			if err != nil || !ok {
				return nil, errors.Join(err, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s:%s not found", provider, extID)))
			}
		}
		e, err := m.loadEntry(ctx, id, lang)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&metav1.LookupExternalResponse{Entry: e}), nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s:%s not found", provider, extID))
}

func (m *Meta) findExternal(ctx context.Context, provider, id string) (uuid.UUID, bool, error) {
	rows, err := m.st.LookupExternalID(ctx, db.LookupExternalIDParams{Provider: provider, ExternalID: id})
	if err != nil || len(rows) == 0 {
		return uuid.Nil, false, err
	}
	target := rows[0].EntryID
	if to, err := m.st.GetRedirect(ctx, target); err == nil {
		target = to
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, err
	}
	return target, true, nil
}

func (m *Meta) lookupOrder(ctx context.Context, provider string) []*host.Provider {
	var out []*host.Provider
	if p, ok := m.host.Get(provider); ok && p.CanLookup(ctx) {
		out = append(out, p)
	}
	for _, p := range m.host.List() {
		if p.ID() == provider || !p.CanLookup(ctx) {
			continue
		}
		for _, c := range p.Info().GetCapabilities() {
			if c == "lookup_by_external_id" {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func (m *Meta) ListTags(ctx context.Context, req *connect.Request[metav1.ListTagsRequest]) (*connect.Response[metav1.ListTagsResponse], error) {
	size := pageSize(req.Msg.GetPageSize(), 200, 1000)
	off, err := decodeOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, err := m.st.ListTagsWithParent(ctx, db.ListTagsWithParentParams{
		GenresOnly: req.Msg.GetGenresOnly(), Prefix: textnorm.Key(req.Msg.GetPrefix()), Lim: int32(size + 1), Off: int32(off),
	})
	if err != nil {
		return nil, err
	}
	out := &metav1.ListTagsResponse{}
	for i, r := range rows {
		if i == size {
			out.NextPageToken = encodeOffset(off + size)
			break
		}
		t := tagProto(r.ID, r.Name, r.Description, r.Level, r.IsGenre, r.IsSpoiler, r.IsExplicit, r.ContentRating, r.EntryCount, r.Data)
		t.Parent = r.ParentName
		out.Tags = append(out.Tags, t)
	}
	return connect.NewResponse(out), nil
}

// refreshIfStale refreshes an entry in the background when its provider
// records are older than StaleAfter and a provider can look them up.
func (m *Meta) refreshIfStale(e *metav1.Entry) {
	if m.opts.StaleAfter <= 0 || e == nil {
		return
	}
	stale := false
	for _, s := range e.GetSources() {
		if s.GetState() != metadatav1.EntryState_ENTRY_STATE_ACTIVE || s.GetFetchedAt() == nil {
			continue
		}
		if time.Since(s.GetFetchedAt().AsTime()) > m.opts.StaleAfter {
			if p, ok := m.host.Get(s.GetRef().GetProvider()); ok && p.Supports(plugin.KindLookup) {
				stale = true
			}
		}
	}
	if !stale {
		return
	}
	id, err := uuid.Parse(e.GetId())
	if err != nil {
		return
	}
	if _, busy := m.refreshing.LoadOrStore(id, struct{}{}); busy {
		return
	}
	select {
	case m.refreshSem <- struct{}{}:
	default:
		m.refreshing.Delete(id)
		return // enough refreshes running; a later read will try again
	}
	go func() {
		defer func() { <-m.refreshSem; m.refreshing.Delete(id) }()
		ctx, cancel := context.WithTimeout(m.opts.Base, 2*time.Minute)
		defer cancel()
		res, err := Refresh(ctx, m.st, m.host, m.writer, id)
		if err != nil {
			m.opts.Log.Warn("refresh stale entry", "entry", id, "err", err)
			return
		}
		m.opts.Log.Debug("refreshed stale entry", "entry", id, "providers", res.GetRefreshed())
	}()
}
