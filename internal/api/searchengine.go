// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/internal/searchsvc"
)

// SetSearch lets the integrations service manage the search engine.
func (h *Integrations) SetSearch(s *searchsvc.Service) { h.search = s }

func (h *Integrations) searchSvc() (*searchsvc.Service, error) {
	if h.search == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("search engine settings aren't available"))
	}
	return h.search, nil
}

func searchErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, searchsvc.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, searchsvc.ErrNoSecrets):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return err
}

func fromSearchSettings(p *metav1.SearchEngineSettings) searchsvc.Settings {
	return searchsvc.Settings{
		Backend: p.GetBackend(), URL: p.GetUrl(), APIKey: p.GetApiKey(), IndexPrefix: p.GetIndexPrefix(), Browse: p.GetBrowse(),
	}
}

// stamp is a timestamp, or nil for the zero time.
func stamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func (h *Integrations) searchState(ctx context.Context) (*metav1.SearchEngineSettings, *metav1.SearchEngineStatus, error) {
	s, err := h.searchSvc()
	if err != nil {
		return nil, nil, err
	}
	st, err := s.Status(ctx)
	if err != nil {
		return nil, nil, err
	}
	set := &metav1.SearchEngineSettings{
		Backend: st.Settings.Backend, Url: st.Settings.URL, HasApiKey: st.KeySet,
		IndexPrefix: st.Settings.IndexPrefix, Browse: st.Settings.Browse, Origin: st.Settings.Origin,
	}
	out := &metav1.SearchEngineStatus{
		Backend: st.Capabilities.Name, TypoTolerance: st.Capabilities.TypoTolerance, Substring: st.Capabilities.Substring,
		Facets: st.Capabilities.Facets, Reachable: st.Reachable, Error: st.ReachErr,
	}
	if e := st.Engine; e != nil {
		out.Ready, out.Degraded, out.Index, out.Documents, out.Pending = e.Ready, e.Degraded, e.Index, e.Documents, e.Pending
		out.Lag = durationpb.New(e.Lag)
		out.BuiltAt = stamp(e.BuiltAt)
		if out.Error == "" && e.LastErr != "" {
			out.Error = e.LastErr
			out.ErrorAt = stamp(e.ErrAt)
		}
	}
	if i := st.Indexer; i != nil {
		out.Applied, out.IndexerError = i.Applied, i.LastErr
		out.IndexerErrorAt = stamp(i.ErrAt)
	}
	return set, out, nil
}

func (h *Integrations) GetSearchEngine(ctx context.Context, _ *connect.Request[metav1.GetSearchEngineRequest]) (*connect.Response[metav1.GetSearchEngineResponse], error) {
	set, st, err := h.searchState(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.GetSearchEngineResponse{Settings: set, Status: st}), nil
}

func (h *Integrations) PutSearchEngine(ctx context.Context, req *connect.Request[metav1.PutSearchEngineRequest]) (*connect.Response[metav1.PutSearchEngineResponse], error) {
	s, err := h.searchSvc()
	if err != nil {
		return nil, err
	}
	if err := s.Save(ctx, fromSearchSettings(req.Msg.GetSettings())); err != nil {
		return nil, searchErr(err)
	}
	set, st, err := h.searchState(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.PutSearchEngineResponse{Settings: set, Status: st}), nil
}

func (h *Integrations) TestSearchEngine(ctx context.Context, req *connect.Request[metav1.TestSearchEngineRequest]) (*connect.Response[metav1.TestSearchEngineResponse], error) {
	s, err := h.searchSvc()
	if err != nil {
		return nil, err
	}
	if err := s.Test(ctx, fromSearchSettings(req.Msg.GetSettings())); err != nil {
		return connect.NewResponse(&metav1.TestSearchEngineResponse{Error: err.Error()}), nil
	}
	return connect.NewResponse(&metav1.TestSearchEngineResponse{Ok: true}), nil
}

func (h *Integrations) ResetSearchEngine(ctx context.Context, _ *connect.Request[metav1.ResetSearchEngineRequest]) (*connect.Response[metav1.ResetSearchEngineResponse], error) {
	s, err := h.searchSvc()
	if err != nil {
		return nil, err
	}
	if err := s.UseConfig(ctx); err != nil {
		return nil, searchErr(err)
	}
	set, st, err := h.searchState(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.ResetSearchEngineResponse{Settings: set, Status: st}), nil
}
