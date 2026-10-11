// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
)

func TestDetailedHealth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "k")
	res, err := f.as(testAdminToken).admin.GetHealth(ctx, connect.NewRequest(&metav1.GetHealthRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*metav1.ComponentHealth{}
	for _, c := range res.Msg.GetComponents() {
		got[c.GetName()] = c
	}
	if res.Msg.GetStatus() != metadatav1.HealthStatus_HEALTH_STATUS_OK || len(got) != 3 {
		t.Fatalf("health: %v", res.Msg)
	}
	if got["database"].GetStatus() != metadatav1.HealthStatus_HEALTH_STATUS_OK || got["search"].GetDetails()["backend"] != "postgres" ||
		got["provider:fake"].GetDetails()["plugin"] != "fake" || got["provider:fake"].GetLatency() == nil {
		t.Fatalf("components: %v", got)
	}
	// A catalogue key doesn't see it.
	created, err := f.as(testAdminToken).clients.CreateClient(ctx, connect.NewRequest(&metav1.CreateClientRequest{Name: "r", Scopes: []string{"catalog:read"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.as(created.Msg.GetApiKey()).admin.GetHealth(ctx, connect.NewRequest(&metav1.GetHealthRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("read key: %v", err)
	}
}

func TestMetricsEndpointAndAPIMetrics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "k")
	admin := f.as(testAdminToken)
	if _, err := admin.meta.Search(ctx, connect.NewRequest(&metav1.SearchRequest{Query: "giants"})); err != nil {
		t.Fatal(err)
	}
	if n := testutil.ToFloat64(f.metrics.APIRequests.WithLabelValues("/panelarr.meta.v1.MetaService/Search", "ok")); n != 1 {
		t.Fatalf("api requests: %v", n)
	}
	if n := testutil.CollectAndCount(f.metrics.SearchDuration); n == 0 {
		t.Fatal("no search latency recorded")
	}
	// The setup synced twice: the runner's metrics are not wired in this
	// fixture, but the plugin calls of the lookup path are.
	scrape := func(token string) (int, string) {
		req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/metrics", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, _ := scrape(""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	reader, err := admin.clients.CreateClient(ctx, connect.NewRequest(&metav1.CreateClientRequest{Name: "r", Scopes: []string{"catalog:read"}}))
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := scrape(reader.Msg.GetApiKey()); code != http.StatusForbidden {
		t.Fatalf("catalog key: %d", code)
	}
	prom, err := admin.clients.CreateClient(ctx, connect.NewRequest(&metav1.CreateClientRequest{Name: "prometheus", Scopes: []string{"metrics:read"}}))
	if err != nil {
		t.Fatal(err)
	}
	code, body := scrape(prom.Msg.GetApiKey())
	if code != http.StatusOK || !strings.Contains(body, `meta_api_requests_total{code="ok",procedure="/panelarr.meta.v1.MetaService/Search"} 1`) ||
		!strings.Contains(body, "go_goroutines") {
		t.Fatalf("scrape: %d\n%s", code, body[:min(len(body), 2000)])
	}
	// The metrics key can't read the catalogue.
	if _, err := f.as(prom.Msg.GetApiKey()).meta.Browse(ctx, connect.NewRequest(&metav1.BrowseRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("metrics key browse: %v", err)
	}
}
