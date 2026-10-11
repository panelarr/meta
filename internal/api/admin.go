// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/enums"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/ingest"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/sdk/plugin"
)

// Admin implements MetaAdminService.
type Admin struct {
	metav1connect.UnimplementedMetaAdminServiceHandler
	st     *store.Store
	host   *host.Host
	runner *ingest.Runner
	writer *catalog.Writer
	// base is the context syncs started from the API run in; it ends when
	// meta shuts down, not when the request ends.
	base context.Context
}

// NewAdmin returns the admin service.
func NewAdmin(base context.Context, st *store.Store, h *host.Host, r *ingest.Runner, w *catalog.Writer) *Admin {
	return &Admin{st: st, host: h, runner: r, writer: w, base: base}
}

func (a *Admin) ListProviders(ctx context.Context, _ *connect.Request[metav1.ListProvidersRequest]) (*connect.Response[metav1.ListProvidersResponse], error) {
	latest, err := a.st.LatestSyncRuns(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := a.st.CountProviderEntries(ctx)
	if err != nil {
		return nil, err
	}
	records := map[string]int64{}
	for _, c := range counts {
		records[c.Provider] = c.Records
	}
	res := &metav1.ListProvidersResponse{}
	for _, p := range a.host.List() {
		health, msg := p.Health()
		pr := &metav1.Provider{
			Id:            p.ID(),
			Mode:          p.Config().Mode(),
			Info:          p.Info(),
			Health:        health,
			HealthMessage: msg,
			Schedules:     p.Config().Schedules,
			RecordCount:   records[p.ID()],
		}
		for _, r := range latest {
			if r.Provider == p.ID() {
				pr.LastRuns = append(pr.LastRuns, syncRunProto(r))
			}
		}
		res.Providers = append(res.Providers, pr)
	}
	return connect.NewResponse(res), nil
}

func (a *Admin) RunSync(ctx context.Context, req *connect.Request[metav1.RunSyncRequest]) (*connect.Response[metav1.RunSyncResponse], error) {
	run, err := a.runner.Start(a.base, req.Msg.GetProvider(), req.Msg.GetKind(), ingest.TriggerManual, req.Msg.GetForce())
	switch {
	case errors.Is(err, ingest.ErrRunning):
		return nil, connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, host.ErrNotConnected):
		return nil, connect.NewError(connect.CodeUnavailable, err)
	case err != nil:
		return nil, err
	}
	return connect.NewResponse(&metav1.RunSyncResponse{Run: syncRunProto(run)}), nil
}

func (a *Admin) ListSyncRuns(ctx context.Context, req *connect.Request[metav1.ListSyncRunsRequest]) (*connect.Response[metav1.ListSyncRunsResponse], error) {
	size := pageSize(req.Msg.GetPageSize(), 20, 100)
	off, err := decodeOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	params := db.ListSyncRunsParams{Lim: int32(size + 1), Off: int32(off)}
	if p := req.Msg.GetProvider(); p != "" {
		params.Provider = pgtype.Text{String: p, Valid: true}
	}
	runs, err := a.st.ListSyncRuns(ctx, params)
	if err != nil {
		return nil, err
	}
	res := &metav1.ListSyncRunsResponse{}
	for i, r := range runs {
		if i == size {
			res.NextPageToken = encodeOffset(off + size)
			break
		}
		res.Runs = append(res.Runs, syncRunProto(r))
	}
	return connect.NewResponse(res), nil
}

func (a *Admin) GetSyncRun(ctx context.Context, req *connect.Request[metav1.GetSyncRunRequest]) (*connect.Response[metav1.GetSyncRunResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid run id %q", req.Msg.GetId()))
	}
	run, err := a.st.GetSyncRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no sync run %s", id))
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.GetSyncRunResponse{Run: syncRunProto(run)}), nil
}

func (a *Admin) RefreshEntry(ctx context.Context, req *connect.Request[metav1.RefreshEntryRequest]) (*connect.Response[metav1.RefreshEntryResponse], error) {
	id, err := resolveEntryID(ctx, a.st, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	res, err := Refresh(ctx, a.st, a.host, a.writer, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(res), nil
}

// Refresh fetches fresh records for an entry from every provider that can
// look them up, and stores them.
func Refresh(ctx context.Context, st *store.Store, h *host.Host, w *catalog.Writer, entry uuid.UUID) (*metav1.RefreshEntryResponse, error) {
	recs, err := st.ListProviderEntriesForEntry(ctx, uuid.NullUUID{UUID: entry, Valid: true})
	if err != nil {
		return nil, err
	}
	res := &metav1.RefreshEntryResponse{Failed: map[string]string{}}
	for _, r := range recs {
		if r.State != "active" {
			continue
		}
		p, ok := h.Get(r.Provider)
		if !ok || !p.CanLookup(ctx) {
			continue
		}
		fresh, err := p.Get(ctx, &metadatav1.ExternalId{Provider: r.Provider, Id: r.ExternalID})
		if err != nil {
			if plugin.ErrorCode(err) == plugin.CodeNotFound {
				_, err = w.Apply(ctx, catalog.Batch{Provider: r.Provider, Removals: []string{r.ExternalID}})
			}
			if err != nil {
				res.Failed[r.Provider] = err.Error()
				continue
			}
			res.Refreshed = append(res.Refreshed, r.Provider)
			continue
		}
		b := catalog.Batch{Provider: r.Provider, Upserts: []*metadatav1.ProviderEntry{fresh}}
		if fresh.GetState() == metadatav1.EntryState_ENTRY_STATE_MERGED && fresh.GetMergedInto().GetId() != "" {
			b.Merges = []catalog.Merge{{Merged: fresh.GetRef().GetId(), Into: fresh.GetMergedInto().GetId()}}
		}
		if _, err := w.Apply(ctx, b); err != nil {
			res.Failed[r.Provider] = err.Error()
			continue
		}
		res.Refreshed = append(res.Refreshed, r.Provider)
	}
	return res, nil
}

func ts(t *timeT) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func syncRunProto(r db.SyncRun) *metav1.SyncRun {
	out := &metav1.SyncRun{
		Id:            r.ID.String(),
		Provider:      r.Provider,
		Kind:          r.Kind,
		Status:        enums.MustParse[metav1.SyncRunStatus](r.Status),
		Trigger:       r.Trigger,
		StartedAt:     timestamppb.New(r.StartedAt),
		FinishedAt:    ts(r.FinishedAt),
		SnapshotId:    r.SnapshotID,
		CursorStart:   r.CursorStart,
		CursorEnd:     r.CursorEnd,
		Upserts:       r.Upserts,
		Changed:       r.Changed,
		Removals:      r.Removals,
		Merges:        r.Merges,
		Skipped:       r.Skipped,
		MarkedRemoved: r.MarkedRemoved,
		Error:         r.Error,
	}
	var notices []struct {
		Level   string `json:"level"`
		Message string `json:"message"`
		Ref     string `json:"ref"`
		At      timeT  `json:"at"`
	}
	if json.Unmarshal(r.Notices, &notices) == nil {
		for _, n := range notices {
			out.Notices = append(out.Notices, &metav1.SyncRunNotice{
				Level:   enums.MustParse[metadatav1.NoticeLevel](n.Level),
				Message: n.Message, Ref: n.Ref, At: timestamppb.New(n.At),
			})
		}
	}
	if len(r.Progress) > 0 {
		var p metadatav1.SyncProgress
		if protojson.Unmarshal(r.Progress, &p) == nil {
			out.Progress, _ = structpb.NewStruct(map[string]any{
				"phase": p.GetPhase(), "done": float64(p.GetDone()), "total": float64(p.GetTotal()), "unit": p.GetUnit(),
			})
		}
	}
	return out
}

// Page tokens are opaque to clients; inside they are offsets.
func encodeOffset(n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(n)))
}

func decodeOffset(tok string) (int, error) {
	if tok == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err == nil && len(b) > 2 && string(b[:2]) == "o:" {
		if n, err := strconv.Atoi(string(b[2:])); err == nil && n >= 0 {
			return n, nil
		}
	}
	return 0, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid page token"))
}

func pageSize(asked int32, def, maxSize int) int {
	switch {
	case asked <= 0:
		return def
	case int(asked) > maxSize:
		return maxSize
	default:
		return int(asked)
	}
}
