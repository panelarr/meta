// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	"github.com/panelarr/meta/internal/integrations"
	"github.com/panelarr/meta/internal/searchsvc"
)

// Integrations implements MetaIntegrationsService.
type Integrations struct {
	metav1connect.UnimplementedMetaIntegrationsServiceHandler
	svc    *integrations.Service
	search *searchsvc.Service
}

// NewIntegrations returns the integrations service.
func NewIntegrations(svc *integrations.Service) *Integrations { return &Integrations{svc: svc} }

func integrationsErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, integrations.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, integrations.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, integrations.ErrInUse), errors.Is(err, integrations.ErrNoSecrets):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return err
	}
}

func (h *Integrations) ListSolvers(ctx context.Context, _ *connect.Request[metav1.ListSolversRequest]) (*connect.Response[metav1.ListSolversResponse], error) {
	s, err := h.svc.ListSolvers(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.ListSolversResponse{Solvers: s}), nil
}

func (h *Integrations) PutSolver(ctx context.Context, req *connect.Request[metav1.PutSolverRequest]) (*connect.Response[metav1.PutSolverResponse], error) {
	s, created, err := h.svc.PutSolver(ctx, req.Msg.GetSolver())
	if err != nil {
		return nil, integrationsErr(err)
	}
	return connect.NewResponse(&metav1.PutSolverResponse{Solver: s, CreatedDefaultProfileId: created}), nil
}

func (h *Integrations) DeleteSolver(ctx context.Context, req *connect.Request[metav1.DeleteSolverRequest]) (*connect.Response[metav1.DeleteSolverResponse], error) {
	return connect.NewResponse(&metav1.DeleteSolverResponse{}), integrationsErr(h.svc.DeleteSolver(ctx, req.Msg.GetSolverId()))
}

func (h *Integrations) ListProfiles(ctx context.Context, _ *connect.Request[metav1.ListProfilesRequest]) (*connect.Response[metav1.ListProfilesResponse], error) {
	p, def, err := h.svc.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.ListProfilesResponse{Profiles: p, DefaultProfileId: def}), nil
}

func (h *Integrations) PutProfile(ctx context.Context, req *connect.Request[metav1.PutProfileRequest]) (*connect.Response[metav1.PutProfileResponse], error) {
	p, err := h.svc.PutProfile(ctx, req.Msg.GetProfile())
	if err != nil {
		return nil, integrationsErr(err)
	}
	return connect.NewResponse(&metav1.PutProfileResponse{Profile: p}), nil
}

func (h *Integrations) DeleteProfile(ctx context.Context, req *connect.Request[metav1.DeleteProfileRequest]) (*connect.Response[metav1.DeleteProfileResponse], error) {
	return connect.NewResponse(&metav1.DeleteProfileResponse{}), integrationsErr(h.svc.DeleteProfile(ctx, req.Msg.GetProfileId()))
}

func (h *Integrations) ListAssignments(ctx context.Context, _ *connect.Request[metav1.ListAssignmentsRequest]) (*connect.Response[metav1.ListAssignmentsResponse], error) {
	a, def, err := h.svc.ListAssignments(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.ListAssignmentsResponse{Assignments: a, DefaultProfileId: def}), nil
}

func (h *Integrations) AssignProfile(ctx context.Context, req *connect.Request[metav1.AssignProfileRequest]) (*connect.Response[metav1.AssignProfileResponse], error) {
	a, err := h.svc.AssignProfile(ctx, req.Msg.GetProviderId(), req.Msg.GetProfileId())
	if err != nil {
		return nil, integrationsErr(err)
	}
	return connect.NewResponse(&metav1.AssignProfileResponse{Assignment: a}), nil
}

func (h *Integrations) SetDefaultProfile(ctx context.Context, req *connect.Request[metav1.SetDefaultProfileRequest]) (*connect.Response[metav1.SetDefaultProfileResponse], error) {
	return connect.NewResponse(&metav1.SetDefaultProfileResponse{}), integrationsErr(h.svc.SetDefaultProfile(ctx, req.Msg.GetProfileId()))
}

func (h *Integrations) SetPushedProfile(ctx context.Context, req *connect.Request[metav1.SetPushedProfileRequest]) (*connect.Response[metav1.SetPushedProfileResponse], error) {
	return connect.NewResponse(&metav1.SetPushedProfileResponse{}), integrationsErr(h.svc.SetPushedProfile(ctx, req.Msg.GetProviderId(), req.Msg.GetProfile()))
}
