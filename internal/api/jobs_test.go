// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
)

// rest calls the REST API as the admin and decodes the JSON answer.
func (f *fixture) rest(t *testing.T, method, path string, out any) int {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	// POSTs go without a body, as the admin UI sends them when every field
	// is in the path.
	res, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return res.StatusCode
}

func waitRun(t *testing.T, c metav1connect.MetaJobsServiceClient, id string) *metav1.JobRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := c.GetJobRun(context.Background(), connect.NewRequest(&metav1.GetJobRunRequest{RunId: id}))
		if err != nil {
			t.Fatal(err)
		}
		if res.Msg.GetRun().GetSummary().GetStatus() != metav1.JobRunStatus_JOB_RUN_STATUS_RUNNING {
			return res.Msg.GetRun()
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never finished", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestJobsListAndSyncHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "config-key")
	c := f.as(testAdminToken).jobs

	list, err := c.ListJobs(ctx, connect.NewRequest(&metav1.ListJobsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*metav1.Job{}
	for _, j := range list.Msg.GetJobs() {
		byID[j.GetId()] = j
	}
	sync := byID["sync.fake.dump"]
	if sync == nil || byID["hold"] == nil {
		t.Fatalf("jobs: %v", list.Msg.GetJobs())
	}
	// The fixture synced twice; the sync job shows the latest run.
	if sync.GetGroup() != "Providers" || !sync.GetCancellable() || sync.GetRunning() ||
		sync.GetLastRun().GetStatus() != metav1.JobRunStatus_JOB_RUN_STATUS_SUCCEEDED ||
		sync.GetLastRun().GetCounts()["upserts"] == 0 || sync.GetSchedule().GetEnabled() || sync.GetNextRunAt() != nil ||
		len(sync.GetParams()) != 1 || sync.GetParams()[0].GetName() != "force" {
		t.Fatalf("sync job: %v", sync)
	}

	// REST, with the dotted job ID in the path.
	var got struct {
		Job struct {
			ID      string `json:"id"`
			LastRun struct {
				Status  string            `json:"status"`
				Trigger string            `json:"trigger"`
				Counts  map[string]string `json:"counts"`
			} `json:"lastRun"`
		} `json:"job"`
	}
	if code := f.rest(t, http.MethodGet, "/v1/admin/jobs/sync.fake.dump", &got); code != http.StatusOK ||
		got.Job.ID != "sync.fake.dump" || got.Job.LastRun.Status != "JOB_RUN_STATUS_SUCCEEDED" || got.Job.LastRun.Trigger != "JOB_TRIGGER_MANUAL" {
		t.Fatalf("GET job: %d %+v", code, got)
	}
	var runs struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
		NextPageToken string `json:"nextPageToken"`
	}
	if code := f.rest(t, http.MethodGet, "/v1/admin/jobs/sync.fake.dump/runs?pageSize=1", &runs); code != http.StatusOK || len(runs.Runs) != 1 || runs.NextPageToken == "" {
		t.Fatalf("GET runs: %d %+v", code, runs)
	}
	var page2 struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	f.rest(t, http.MethodGet, "/v1/admin/jobs/sync.fake.dump/runs?pageSize=1&pageToken="+runs.NextPageToken, &page2)
	if len(page2.Runs) != 1 || page2.Runs[0].ID == runs.Runs[0].ID {
		t.Fatalf("second page: %+v", page2)
	}
	var none struct {
		Runs []any `json:"runs"`
	}
	f.rest(t, http.MethodGet, "/v1/admin/jobs/sync.fake.dump/runs?status=JOB_RUN_STATUS_FAILED", &none)
	if len(none.Runs) != 0 {
		t.Fatalf("failed runs: %+v", none)
	}
	run := waitRun(t, c, runs.Runs[0].ID)
	if run.GetSummary().GetJobId() != "sync.fake.dump" || run.GetParams()["force"] != "false" {
		t.Fatalf("sync run: %v", run)
	}

	// Sync now through the jobs API, with force.
	started, err := c.RunJob(ctx, connect.NewRequest(&metav1.RunJobRequest{JobId: "sync.fake.dump", Params: map[string]string{"force": "true"}}))
	if err != nil {
		t.Fatal(err)
	}
	done := waitRun(t, c, started.Msg.GetRun().GetId())
	if done.GetSummary().GetStatus() != metav1.JobRunStatus_JOB_RUN_STATUS_SUCCEEDED || done.GetParams()["force"] != "true" ||
		done.GetSummary().GetTriggeredBy() != "admin" || done.GetSummary().GetTrigger() != metav1.JobTrigger_JOB_TRIGGER_MANUAL {
		t.Fatalf("sync through jobs: %v", done)
	}
}

func TestRunJobBusyCancelAndErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "config-key")
	c := f.as(testAdminToken).jobs

	first, err := c.RunJob(ctx, connect.NewRequest(&metav1.RunJobRequest{JobId: "hold"}))
	if err != nil {
		t.Fatal(err)
	}
	id := first.Msg.GetRun().GetId()

	// Already running: 409 with the current run in the detail.
	_, err = c.RunJob(ctx, connect.NewRequest(&metav1.RunJobRequest{JobId: "hold"}))
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeAlreadyExists {
		t.Fatalf("second run: %v", err)
	}
	var detail *metav1.JobAlreadyRunning
	for _, d := range cerr.Details() {
		if m, err := d.Value(); err == nil {
			if v, ok := m.(*metav1.JobAlreadyRunning); ok {
				detail = v
			}
		}
	}
	if detail.GetCurrentRunId() != id {
		t.Fatalf("detail: %v", detail)
	}
	var busy map[string]any
	if code := f.rest(t, http.MethodPost, "/v1/admin/jobs/hold:run", &busy); code != http.StatusConflict {
		t.Fatalf("REST second run: %d %v", code, busy)
	}

	job, err := c.GetJob(ctx, connect.NewRequest(&metav1.GetJobRequest{JobId: "hold"}))
	if err != nil || !job.Msg.GetJob().GetRunning() || job.Msg.GetJob().GetCurrentRunId() != id {
		t.Fatalf("running job: %v %v", job, err)
	}
	live, err := c.GetJobRun(ctx, connect.NewRequest(&metav1.GetJobRunRequest{RunId: id}))
	if err != nil || live.Msg.GetRun().GetProgress().GetUnit() != "steps" {
		t.Fatalf("progress of a live run: %v %v", live, err)
	}

	if code := f.rest(t, http.MethodPost, "/v1/admin/job-runs/"+id+":cancel", nil); code != http.StatusOK {
		t.Fatalf("REST cancel: %d", code)
	}
	if run := waitRun(t, c, id); run.GetSummary().GetStatus() != metav1.JobRunStatus_JOB_RUN_STATUS_CANCELLED {
		t.Fatalf("cancelled: %v", run)
	}
	if _, err := c.CancelJobRun(ctx, connect.NewRequest(&metav1.CancelJobRunRequest{RunId: id})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("cancel a finished run: %v", err)
	}

	for name, call := range map[string]struct {
		err  error
		want connect.Code
	}{
		"unknown job": {func() error {
			_, err := c.RunJob(ctx, connect.NewRequest(&metav1.RunJobRequest{JobId: "nope"}))
			return err
		}(), connect.CodeNotFound},
		"bad param": {func() error {
			_, err := c.RunJob(ctx, connect.NewRequest(&metav1.RunJobRequest{JobId: "hold", Params: map[string]string{"limit": "lots"}}))
			return err
		}(), connect.CodeInvalidArgument},
		"unknown run": {func() error {
			_, err := c.GetJobRun(ctx, connect.NewRequest(&metav1.GetJobRunRequest{RunId: "0192aa00-0000-7000-8000-000000000000"}))
			return err
		}(), connect.CodeNotFound},
	} {
		if connect.CodeOf(call.err) != call.want {
			t.Errorf("%s: %v, want %v", name, call.err, call.want)
		}
	}
	close(f.release)
}

func TestJobsNeedAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "config-key")
	created, err := f.as(testAdminToken).clients.CreateClient(ctx, connect.NewRequest(&metav1.CreateClientRequest{Name: "reader", Scopes: []string{"catalog:read", "metrics:read"}}))
	if err != nil {
		t.Fatal(err)
	}
	reader := f.as(created.Msg.GetApiKey()).jobs
	if _, err := reader.ListJobs(ctx, connect.NewRequest(&metav1.ListJobsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("list as a reader: %v", err)
	}
	if _, err := reader.RunJob(ctx, connect.NewRequest(&metav1.RunJobRequest{JobId: "hold"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("run as a reader: %v", err)
	}
	if _, err := f.as("").jobs.ListJobs(ctx, connect.NewRequest(&metav1.ListJobsRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("list without a key: %v", err)
	}
}
