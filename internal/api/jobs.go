// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	"github.com/panelarr/meta/internal/auth"
	"github.com/panelarr/meta/internal/jobs"
)

// Jobs implements MetaJobsService.
type Jobs struct {
	metav1connect.UnimplementedMetaJobsServiceHandler
	m *jobs.Manager
	// base is the context runs started from the API run in; it ends when
	// meta shuts down, not when the request ends.
	base context.Context
}

// NewJobs returns the jobs service.
func NewJobs(base context.Context, m *jobs.Manager) *Jobs {
	return &Jobs{m: m, base: base}
}

func (s *Jobs) ListJobs(ctx context.Context, _ *connect.Request[metav1.ListJobsRequest]) (*connect.Response[metav1.ListJobsResponse], error) {
	all := s.m.Jobs()
	last, err := s.m.LastRuns(ctx, all)
	if err != nil {
		return nil, err
	}
	res := &metav1.ListJobsResponse{}
	for _, j := range all {
		pj, err := s.job(ctx, j, last)
		if err != nil {
			return nil, err
		}
		res.Jobs = append(res.Jobs, pj)
	}
	return connect.NewResponse(res), nil
}

func (s *Jobs) GetJob(ctx context.Context, req *connect.Request[metav1.GetJobRequest]) (*connect.Response[metav1.GetJobResponse], error) {
	j, err := s.find(req.Msg.GetJobId())
	if err != nil {
		return nil, err
	}
	last := map[string]jobs.Summary{}
	if l, err := s.m.Last(ctx, j); err != nil {
		return nil, err
	} else if l != nil {
		last[j.Def().ID] = *l
	}
	pj, err := s.job(ctx, j, last)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&metav1.GetJobResponse{Job: pj}), nil
}

func (s *Jobs) ListJobRuns(ctx context.Context, req *connect.Request[metav1.ListJobRunsRequest]) (*connect.Response[metav1.ListJobRunsResponse], error) {
	j, err := s.find(req.Msg.GetJobId())
	if err != nil {
		return nil, err
	}
	size := pageSize(req.Msg.GetPageSize(), 20, 100)
	off, err := decodeOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	status := ""
	if st := req.Msg.GetStatus(); st != metav1.JobRunStatus_JOB_RUN_STATUS_UNSPECIFIED {
		status = strings.ToLower(strings.TrimPrefix(st.String(), "JOB_RUN_STATUS_"))
	}
	runs, err := j.Runs(ctx, status, size+1, off)
	if err != nil {
		return nil, err
	}
	res := &metav1.ListJobRunsResponse{}
	for i, r := range runs {
		if i == size {
			res.NextPageToken = encodeOffset(off + size)
			break
		}
		res.Runs = append(res.Runs, runSummaryProto(r))
	}
	return connect.NewResponse(res), nil
}

func (s *Jobs) GetJobRun(ctx context.Context, req *connect.Request[metav1.GetJobRunRequest]) (*connect.Response[metav1.GetJobRunResponse], error) {
	run, err := s.m.GetRun(ctx, req.Msg.GetRunId())
	if err != nil {
		return nil, jobError(err)
	}
	return connect.NewResponse(&metav1.GetJobRunResponse{Run: runProto(run)}), nil
}

func (s *Jobs) RunJob(ctx context.Context, req *connect.Request[metav1.RunJobRequest]) (*connect.Response[metav1.RunJobResponse], error) {
	by := ""
	if p, ok := auth.FromContext(ctx); ok {
		by = p.Name
	}
	run, err := s.m.RunJob(s.base, req.Msg.GetJobId(), jobs.Request{Trigger: jobs.TriggerManual, By: by, Params: req.Msg.GetParams()})
	if err != nil {
		return nil, jobError(err)
	}
	return connect.NewResponse(&metav1.RunJobResponse{Run: runSummaryProto(run)}), nil
}

func (s *Jobs) CancelJobRun(ctx context.Context, req *connect.Request[metav1.CancelJobRunRequest]) (*connect.Response[metav1.CancelJobRunResponse], error) {
	run, err := s.m.Cancel(ctx, req.Msg.GetRunId())
	if err != nil {
		return nil, jobError(err)
	}
	return connect.NewResponse(&metav1.CancelJobRunResponse{Run: runSummaryProto(run)}), nil
}

func (s *Jobs) find(id string) (jobs.Job, error) {
	j, ok := s.m.Job(id)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no job "+id))
	}
	return j, nil
}

func (s *Jobs) job(ctx context.Context, j jobs.Job, last map[string]jobs.Summary) (*metav1.Job, error) {
	def := j.Def()
	cur, running, err := j.Current(ctx)
	if err != nil {
		return nil, err
	}
	pj := &metav1.Job{
		Id: def.ID, Name: def.Name, Description: def.Description, Group: def.Group,
		Schedule: &metav1.Schedule{Enabled: def.Schedule.Enabled, Source: def.Schedule.Source},
		Running:  running, CurrentRunId: cur, Cancellable: def.Cancellable,
	}
	switch {
	case def.Schedule.Cron != "":
		pj.Schedule.Kind = &metav1.Schedule_Cron{Cron: def.Schedule.Cron}
	case def.Schedule.Interval > 0:
		pj.Schedule.Kind = &metav1.Schedule_Interval{Interval: durationpb.New(def.Schedule.Interval)}
	}
	for _, p := range def.Params {
		pj.Params = append(pj.Params, &metav1.JobParam{Name: p.Name, Type: p.Type, Description: p.Description, Required: p.Required, Default: p.Default})
	}
	if l, ok := last[def.ID]; ok {
		pj.LastRun = runSummaryProto(l)
	}
	if next, ok := s.m.NextRun(def.ID); ok {
		pj.NextRunAt = timestamppb.New(next)
	}
	return pj, nil
}

// jobError maps the jobs package's errors onto API codes.
func jobError(err error) error {
	var busy *jobs.RunningError
	switch {
	case errors.As(err, &busy):
		cerr := connect.NewError(connect.CodeAlreadyExists, err)
		if d, derr := connect.NewErrorDetail(&metav1.JobAlreadyRunning{CurrentRunId: busy.RunID}); derr == nil {
			cerr.AddDetail(d)
		}
		return cerr
	case errors.Is(err, jobs.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, jobs.ErrInvalidParam):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, jobs.ErrNotCancellable), errors.Is(err, jobs.ErrNotRunning):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return err
}

var runStatuses = map[string]metav1.JobRunStatus{
	jobs.StatusRunning:   metav1.JobRunStatus_JOB_RUN_STATUS_RUNNING,
	jobs.StatusSucceeded: metav1.JobRunStatus_JOB_RUN_STATUS_SUCCEEDED,
	jobs.StatusFailed:    metav1.JobRunStatus_JOB_RUN_STATUS_FAILED,
	jobs.StatusCancelled: metav1.JobRunStatus_JOB_RUN_STATUS_CANCELLED,
	jobs.StatusSkipped:   metav1.JobRunStatus_JOB_RUN_STATUS_SKIPPED,
}

var triggers = map[string]metav1.JobTrigger{
	jobs.TriggerSchedule: metav1.JobTrigger_JOB_TRIGGER_SCHEDULE,
	jobs.TriggerManual:   metav1.JobTrigger_JOB_TRIGGER_MANUAL,
	jobs.TriggerStartup:  metav1.JobTrigger_JOB_TRIGGER_STARTUP,
	jobs.TriggerSystem:   metav1.JobTrigger_JOB_TRIGGER_SYSTEM,
}

func runSummaryProto(r jobs.Summary) *metav1.JobRunSummary {
	p := &metav1.JobRunSummary{
		Id: r.ID, JobId: r.JobID, Status: runStatuses[r.Status], Trigger: triggers[r.Trigger],
		StartedAt: timestamppb.New(r.StartedAt), Message: r.Message, Counts: r.Counts, TriggeredBy: r.TriggeredBy,
	}
	if r.FinishedAt != nil {
		p.FinishedAt = timestamppb.New(*r.FinishedAt)
	}
	return p
}

func runProto(r jobs.Run) *metav1.JobRun {
	p := &metav1.JobRun{Summary: runSummaryProto(r.Summary), Params: r.Params}
	for _, n := range r.Notes {
		p.Notes = append(p.Notes, &metav1.JobRunNote{At: timestamppb.New(n.At), Level: n.Level, Message: n.Message, Ref: n.Ref})
	}
	if r.Progress != nil {
		p.Progress = &metav1.JobProgress{Phase: r.Progress.Phase, Done: r.Progress.Done, Total: r.Progress.Total, Unit: r.Progress.Unit}
	}
	return p
}
