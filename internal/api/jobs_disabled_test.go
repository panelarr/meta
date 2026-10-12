// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	"github.com/panelarr/meta/internal/api"
	"github.com/panelarr/meta/internal/catalog"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/ingest"
	"github.com/panelarr/meta/internal/jobs"
	"github.com/panelarr/meta/internal/search"
	"github.com/panelarr/meta/internal/store/storetest"
	"github.com/panelarr/meta/sdk/client"
)

// TestDisabledJob: a job that can't run now says why, has no next run, and
// RunJob answers failed_precondition with ErrorInfo JOB_DISABLED (400
// over REST); once the reason is gone it runs.
func TestDisabledJob(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	jm := jobs.New(st, log, 100)
	var off atomic.Bool
	off.Store(true)
	const why = "Only with a search engine (Typesense or Meilisearch)."
	jm.Add(jobs.Def{
		ID: "gated", Name: "Gated", Schedule: jobs.Schedule{Interval: time.Hour, Enabled: true, Source: "test"},
		Disabled: func(context.Context) string {
			if off.Load() {
				return why
			}
			return ""
		},
	}, func(context.Context, *jobs.Recorder) error { return nil })
	if err := jm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(jm.Stop)
	t.Cleanup(jm.Wait)
	cfg := config.Default()
	h := host.New(cfg, st, log, func(context.Context, config.Provider, *slog.Logger) (client.Client, error) {
		return nil, errors.New("no plugins here")
	})
	w := catalog.NewWriter(st, catalog.Options{Priority: catalog.Priority{Fallback: cfg.ProviderOrder()}, DisplayLanguage: "en"})
	r := ingest.NewRunner(st, h, w, log, 100)
	srv, err := api.New(api.Services{
		Meta:    api.NewMeta(st, h, w, api.MetaOptions{DisplayLanguage: "en", Log: log, Index: search.NewPostgres(st.Pool)}),
		Admin:   api.NewAdmin(ctx, st, h, r, w),
		Clients: api.NewClients(st),
		Jobs:    api.NewJobs(ctx, jm),
	}, api.Options{Log: log})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	c := metav1connect.NewMetaJobsServiceClient(hs.Client(), hs.URL)

	got, err := c.GetJob(ctx, connect.NewRequest(&metav1.GetJobRequest{JobId: "gated"}))
	if err != nil {
		t.Fatal(err)
	}
	if j := got.Msg.GetJob(); j.GetDisabledReason() != why || j.GetNextRunAt() != nil || !j.GetSchedule().GetEnabled() {
		t.Fatalf("disabled job: %v", j)
	}

	_, err = c.RunJob(ctx, connect.NewRequest(&metav1.RunJobRequest{JobId: "gated"}))
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("RunJob: %v", err)
	}
	var info *errdetails.ErrorInfo
	for _, d := range cerr.Details() {
		if v, err := d.Value(); err == nil {
			if i, ok := v.(*errdetails.ErrorInfo); ok {
				info = i
			}
		}
	}
	if info.GetReason() != api.ReasonJobDisabled || info.GetDomain() != "panelarr.meta" || info.GetMetadata()["job_id"] != "gated" {
		t.Fatalf("error info: %v", info)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, hs.URL+"/v1/admin/jobs/gated:run", nil)
	res, err := hs.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest { // failed_precondition over REST
		t.Fatalf("REST status %d", res.StatusCode)
	}

	off.Store(false)
	got, err = c.GetJob(ctx, connect.NewRequest(&metav1.GetJobRequest{JobId: "gated"}))
	if err != nil || got.Msg.GetJob().GetDisabledReason() != "" || got.Msg.GetJob().GetNextRunAt() == nil {
		t.Fatalf("enabled again: %v %v", got, err)
	}
	if _, err := c.RunJob(ctx, connect.NewRequest(&metav1.RunJobRequest{JobId: "gated"})); err != nil {
		t.Fatal(err)
	}
}
