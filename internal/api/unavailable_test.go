// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

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

// providerInfo returns the ErrorInfo of a "provider unavailable" error.
func providerInfo(t *testing.T, err error) *errdetails.ErrorInfo {
	t.Helper()
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeUnavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
	for _, d := range cerr.Details() {
		if v, err := d.Value(); err == nil {
			if info, ok := v.(*errdetails.ErrorInfo); ok {
				return info
			}
		}
	}
	t.Fatalf("no ErrorInfo on %v", err)
	return nil
}

// TestProviderUnavailableErrorInfo: calls that fail because a provider's
// plugin can't be reached say which provider, with the same code and
// message as before.
func TestProviderUnavailableErrorInfo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := storetest.New(t)
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "downprov", Remote: &config.Remote{URL: "x"}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := host.New(cfg, st, log, func(context.Context, config.Provider, *slog.Logger) (client.Client, error) {
		return nil, errors.New("connection refused")
	})
	w := catalog.NewWriter(st, catalog.Options{Priority: catalog.Priority{Fallback: cfg.ProviderOrder()}, DisplayLanguage: "en"})
	r := ingest.NewRunner(st, h, w, log, 100)
	jm := jobs.New(st, log, 100)
	jm.AddSource(ingest.NewSyncJobs(r))
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

	admin := metav1connect.NewMetaAdminServiceClient(hs.Client(), hs.URL)
	_, err = admin.RunSync(ctx, connect.NewRequest(&metav1.RunSyncRequest{Provider: "downprov", Kind: "dump"}))
	info := providerInfo(t, err)
	if info.GetReason() != api.ReasonProviderUnavailable || info.GetDomain() != "panelarr.meta" || info.GetMetadata()["provider"] != "downprov" {
		t.Fatalf("RunSync ErrorInfo: %v", info)
	}
	if msg := connect.NewError(connect.CodeUnknown, err).Message(); msg == "" {
		t.Fatal("no message")
	}
	var cerr *connect.Error
	if errors.As(err, &cerr); cerr.Message() != "plugin is not connected: connection refused" {
		t.Fatalf("message changed: %q", cerr.Message())
	}

	meta := metav1connect.NewMetaServiceClient(hs.Client(), hs.URL)
	_, err = meta.LookupExternal(ctx, connect.NewRequest(&metav1.LookupExternalRequest{Provider: "downprov", ExternalId: "1"}))
	if info := providerInfo(t, err); info.GetMetadata()["provider"] != "downprov" {
		t.Fatalf("LookupExternal ErrorInfo: %v", info)
	}

	jobsc := metav1connect.NewMetaJobsServiceClient(hs.Client(), hs.URL)
	_, err = jobsc.RunJob(ctx, connect.NewRequest(&metav1.RunJobRequest{JobId: "sync.downprov.dump"}))
	if info := providerInfo(t, err); info.GetReason() != api.ReasonProviderUnavailable {
		t.Fatalf("RunJob ErrorInfo: %v", info)
	}
}
