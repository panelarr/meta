// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/timestamppb"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	"github.com/panelarr/meta/internal/auth"
	"github.com/panelarr/meta/internal/duplicates"
	"github.com/panelarr/meta/internal/store"
	"github.com/panelarr/meta/internal/store/db"
)

// Duplicates implements MetaDuplicatesService.
type Duplicates struct {
	metav1connect.UnimplementedMetaDuplicatesServiceHandler
	st   *store.Store
	svc  *duplicates.Service
	meta *Meta
}

// NewDuplicates returns the duplicates service. meta builds the entry
// summaries of each pair.
func NewDuplicates(st *store.Store, svc *duplicates.Service, meta *Meta) *Duplicates {
	return &Duplicates{st: st, svc: svc, meta: meta}
}

func dupErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, duplicates.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, duplicates.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, duplicates.ErrDecided), errors.Is(err, duplicates.ErrNoSecrets):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return err
}

var dupStatuses = map[string]metav1.DuplicateStatus{
	"open":      metav1.DuplicateStatus_DUPLICATE_STATUS_OPEN,
	"confirmed": metav1.DuplicateStatus_DUPLICATE_STATUS_CONFIRMED,
	"dismissed": metav1.DuplicateStatus_DUPLICATE_STATUS_DISMISSED,
}

func principalName(ctx context.Context) string {
	if p, ok := auth.FromContext(ctx); ok {
		return p.Name
	}
	return ""
}

func (h *Duplicates) ListDuplicates(ctx context.Context, req *connect.Request[metav1.ListDuplicatesRequest]) (*connect.Response[metav1.ListDuplicatesResponse], error) {
	r := req.Msg
	size := pageSize(r.GetPageSize(), 20, 100)
	off, err := decodeOffset(r.GetPageToken())
	if err != nil {
		return nil, err
	}
	status := "open"
	if r.GetStatus() != metav1.DuplicateStatus_DUPLICATE_STATUS_UNSPECIFIED {
		status = strings.ToLower(strings.TrimPrefix(r.GetStatus().String(), "DUPLICATE_STATUS_"))
	}
	q := db.New(h.st.Pool)
	st := pgtype.Text{String: status, Valid: true}
	rows, err := q.ListDuplicateCandidates(ctx, db.ListDuplicateCandidatesParams{
		Status: st, MinScore: r.GetMinScore(), JudgedOnly: r.GetJudgedOnly(), Lim: int32(size + 1), Off: int32(off), //nolint:gosec // small page sizes
	})
	if err != nil {
		return nil, err
	}
	total, err := q.CountDuplicateCandidates(ctx, db.CountDuplicateCandidatesParams{Status: st, MinScore: r.GetMinScore(), JudgedOnly: r.GetJudgedOnly()})
	if err != nil {
		return nil, err
	}
	res := &metav1.ListDuplicatesResponse{TotalSize: total}
	if len(rows) > size {
		rows = rows[:size]
		res.NextPageToken = encodeOffset(off + size)
	}
	if res.Pairs, err = h.pairs(ctx, rows); err != nil {
		return nil, err
	}
	return connect.NewResponse(res), nil
}

func (h *Duplicates) GetDuplicate(ctx context.Context, req *connect.Request[metav1.GetDuplicateRequest]) (*connect.Response[metav1.GetDuplicateResponse], error) {
	c, err := db.New(h.st.Pool).GetDuplicateCandidate(ctx, req.Msg.GetId())
	if err != nil {
		return nil, dupErr(notFound(err, "no duplicate pair"))
	}
	p, err := h.pair(ctx, c)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.GetDuplicateResponse{Pair: p}), nil
}

func notFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", duplicates.ErrNotFound, what)
	}
	return err
}

func (h *Duplicates) ConfirmDuplicate(ctx context.Context, req *connect.Request[metav1.ConfirmDuplicateRequest]) (*connect.Response[metav1.ConfirmDuplicateResponse], error) {
	c, into, err := h.svc.Confirm(ctx, req.Msg.GetId(), req.Msg.GetKeep(), principalName(ctx), req.Msg.GetNote())
	if err != nil {
		return nil, dupErr(err)
	}
	p, err := h.pair(ctx, c)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.ConfirmDuplicateResponse{Pair: p, EntryId: into.String()}), nil
}

func (h *Duplicates) DismissDuplicate(ctx context.Context, req *connect.Request[metav1.DismissDuplicateRequest]) (*connect.Response[metav1.DismissDuplicateResponse], error) {
	c, err := h.svc.Dismiss(ctx, req.Msg.GetId(), principalName(ctx), req.Msg.GetNote())
	if err != nil {
		return nil, dupErr(err)
	}
	p, err := h.pair(ctx, c)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.DismissDuplicateResponse{Pair: p}), nil
}

func (h *Duplicates) ReopenDuplicate(ctx context.Context, req *connect.Request[metav1.ReopenDuplicateRequest]) (*connect.Response[metav1.ReopenDuplicateResponse], error) {
	c, err := h.svc.Reopen(ctx, req.Msg.GetId())
	if err != nil {
		return nil, dupErr(err)
	}
	p, err := h.pair(ctx, c)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.ReopenDuplicateResponse{Pair: p}), nil
}

func (h *Duplicates) pair(ctx context.Context, c db.DuplicateCandidate) (*metav1.DuplicatePair, error) {
	ps, err := h.pairs(ctx, []db.DuplicateCandidate{c})
	if err != nil {
		return nil, err
	}
	return ps[0], nil
}

// pairs builds the pairs with both entries' summaries, in one go.
func (h *Duplicates) pairs(ctx context.Context, rows []db.DuplicateCandidate) ([]*metav1.DuplicatePair, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, 2*len(rows))
	for _, c := range rows {
		ids = append(ids, c.EntryA, c.EntryB)
	}
	sums, err := h.meta.summaries(ctx, ids, h.meta.opts.DisplayLanguage)
	if err != nil {
		return nil, err
	}
	out := make([]*metav1.DuplicatePair, 0, len(rows))
	for i, c := range rows {
		p := &metav1.DuplicatePair{
			Id: c.ID, EntryA: sums[2*i], EntryB: sums[2*i+1], Status: dupStatuses[c.Status], Score: c.Score,
			CreatedAt: timestamppb.New(c.CreatedAt), UpdatedAt: timestamppb.New(c.UpdatedAt),
			DecidedBy: c.DecidedBy, Note: c.Note,
		}
		if c.DecidedAt != nil {
			p.DecidedAt = timestamppb.New(*c.DecidedAt)
		}
		var sig duplicates.Signals
		if json.Unmarshal(c.Signals, &sig) == nil {
			p.Signals = &metav1.DuplicateSignals{
				Title: sig.Title, TitleA: sig.TitleA, TitleB: sig.TitleB, People: sig.People, SharedPeople: sig.SharedPeople,
				YearA: int32(sig.YearA), YearB: int32(sig.YearB), TypeA: sig.TypeA, TypeB: sig.TypeB, //nolint:gosec // years
				LanguageA: sig.LanguageA, LanguageB: sig.LanguageB, SharedIds: sig.SharedIDs, SameProvider: sig.SameProvider,
			}
		}
		if len(c.Verdict) > 0 {
			var v duplicates.Verdict
			if json.Unmarshal(c.Verdict, &v) == nil {
				p.Verdict = verdictProto(v)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func verdictProto(v duplicates.Verdict) *metav1.DuplicateVerdict {
	pv := &metav1.DuplicateVerdict{
		Judge: v.Judge, Model: v.Model, Same: v.Same, Choice: v.Choice, Probabilities: v.Probabilities,
		Confidence: v.Confidence, Tokens: v.Tokens,
	}
	if !v.At.IsZero() {
		pv.At = timestamppb.New(v.At)
	}
	return pv
}

func (h *Duplicates) judgeProto(ctx context.Context) (*metav1.DecisionJudgeSettings, error) {
	js, err := h.svc.JudgeSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &metav1.DecisionJudgeSettings{
		Enabled: js.Enabled, Kind: js.Kind, Url: js.URL, Model: js.Model, HasApiKey: js.APIKey != "", Covers: js.Covers, Origin: js.Origin,
	}, nil
}

func fromJudgeProto(p *metav1.DecisionJudgeSettings) duplicates.JudgeSettings {
	return duplicates.JudgeSettings{
		Enabled: p.GetEnabled(), Kind: p.GetKind(), URL: strings.TrimSpace(p.GetUrl()), Model: strings.TrimSpace(p.GetModel()),
		APIKey: p.GetApiKey(), Covers: p.GetCovers(),
	}
}

func (h *Duplicates) GetDecisionJudge(ctx context.Context, _ *connect.Request[metav1.GetDecisionJudgeRequest]) (*connect.Response[metav1.GetDecisionJudgeResponse], error) {
	s, err := h.judgeProto(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.GetDecisionJudgeResponse{Settings: s}), nil
}

func (h *Duplicates) PutDecisionJudge(ctx context.Context, req *connect.Request[metav1.PutDecisionJudgeRequest]) (*connect.Response[metav1.PutDecisionJudgeResponse], error) {
	if err := h.svc.SaveJudge(ctx, fromJudgeProto(req.Msg.GetSettings())); err != nil {
		return nil, dupErr(err)
	}
	s, err := h.judgeProto(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.PutDecisionJudgeResponse{Settings: s}), nil
}

func (h *Duplicates) TestDecisionJudge(ctx context.Context, req *connect.Request[metav1.TestDecisionJudgeRequest]) (*connect.Response[metav1.TestDecisionJudgeResponse], error) {
	v, err := h.svc.TestJudge(ctx, fromJudgeProto(req.Msg.GetSettings()))
	if err != nil {
		return connect.NewResponse(&metav1.TestDecisionJudgeResponse{Error: err.Error()}), nil
	}
	return connect.NewResponse(&metav1.TestDecisionJudgeResponse{Ok: true, Verdict: verdictProto(v)}), nil
}

func (h *Duplicates) ResetDecisionJudge(ctx context.Context, _ *connect.Request[metav1.ResetDecisionJudgeRequest]) (*connect.Response[metav1.ResetDecisionJudgeResponse], error) {
	if err := h.svc.UseConfigJudge(ctx); err != nil {
		return nil, err
	}
	s, err := h.judgeProto(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.ResetDecisionJudgeResponse{Settings: s}), nil
}
