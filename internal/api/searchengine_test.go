// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
)

// TestSearchEngineSettingsAndFallback configures an engine that isn't
// there: the key is stored sealed and never returned, searches fall back to
// PostgreSQL, health says degraded and meta stays ready.
func TestSearchEngineSettingsAndFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "config-key")
	c := f.as(testAdminToken).integrations

	got, err := c.GetSearchEngine(ctx, connect.NewRequest(&metav1.GetSearchEngineRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Msg.GetSettings(); s.GetBackend() != "postgres" || s.GetOrigin() != "config" || got.Msg.GetStatus().GetFacets() {
		t.Fatalf("default: %v", got.Msg)
	}

	// Validation.
	if _, err := c.PutSearchEngine(ctx, connect.NewRequest(&metav1.PutSearchEngineRequest{Settings: &metav1.SearchEngineSettings{Backend: "typesense", Url: "not a url"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad url: %v", err)
	}
	if _, err := c.PutSearchEngine(ctx, connect.NewRequest(&metav1.PutSearchEngineRequest{Settings: &metav1.SearchEngineSettings{Backend: "elastic"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown backend: %v", err)
	}
	test, err := c.TestSearchEngine(ctx, connect.NewRequest(&metav1.TestSearchEngineRequest{Settings: &metav1.SearchEngineSettings{Backend: "typesense", Url: "http://127.0.0.1:1"}}))
	if err != nil || test.Msg.GetOk() || test.Msg.GetError() == "" {
		t.Fatalf("test of a missing engine: %v %v", test, err)
	}

	const key = "ts-secret-key-1234"
	put, err := c.PutSearchEngine(ctx, connect.NewRequest(&metav1.PutSearchEngineRequest{Settings: &metav1.SearchEngineSettings{
		Backend: "typesense", Url: "http://127.0.0.1:1", ApiKey: key,
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if s := put.Msg.GetSettings(); s.GetBackend() != "typesense" || !s.GetHasApiKey() || s.GetApiKey() != "" || s.GetOrigin() != "admin" {
		t.Fatalf("saved: %v", s)
	}
	if st := put.Msg.GetStatus(); st.GetReachable() || st.GetReady() || st.GetError() == "" || !st.GetFacets() {
		t.Fatalf("status of a missing engine: %v", st)
	}
	// Sealed at rest.
	var sealed string
	if err := f.st.Pool.QueryRow(ctx, `SELECT api_key_sealed FROM search_settings`).Scan(&sealed); err != nil || sealed == "" || strings.Contains(sealed, key) {
		t.Fatalf("stored key: %q %v", sealed, err)
	}
	// Never returned, not even over REST.
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/v1/admin/search-engine", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	res, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || strings.Contains(string(body), key) || !strings.Contains(string(body), `"hasApiKey":true`) {
		t.Fatalf("REST: %d %s", res.StatusCode, body)
	}
	// Saving again without a key keeps it.
	if _, err := c.PutSearchEngine(ctx, connect.NewRequest(&metav1.PutSearchEngineRequest{Settings: &metav1.SearchEngineSettings{
		Backend: "typesense", Url: "http://127.0.0.1:1", Browse: "engine",
	}})); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.GetSearchEngine(ctx, connect.NewRequest(&metav1.GetSearchEngineRequest{})); !got.Msg.GetSettings().GetHasApiKey() || got.Msg.GetSettings().GetBrowse() != "engine" {
		t.Fatalf("key kept: %v", got.Msg.GetSettings())
	}

	// Searches still work, from PostgreSQL.
	hits, err := f.as(testAdminToken).meta.Search(ctx, connect.NewRequest(&metav1.SearchRequest{Query: "lantern", Facets: true}))
	if err != nil || len(hits.Msg.GetHits()) != 1 || len(hits.Msg.GetFacets()) != 0 {
		t.Fatalf("fallback search: %v %v", hits, err)
	}
	health, err := f.as(testAdminToken).admin.GetHealth(ctx, connect.NewRequest(&metav1.GetHealthRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var search *metav1.ComponentHealth
	for _, comp := range health.Msg.GetComponents() {
		if comp.GetName() == "search" {
			search = comp
		}
	}
	if search.GetStatus() != metadatav1.HealthStatus_HEALTH_STATUS_DEGRADED || !strings.Contains(search.GetMessage(), "PostgreSQL answers") ||
		search.GetDetails()["backend"] != "typesense" {
		t.Fatalf("search health: %v", search)
	}
	r, err := http.Get(f.srv.URL + "/readyz") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("readyz with the engine down: %d", r.StatusCode)
	}

	// Back to the configuration: PostgreSQL, the outbox stops and empties.
	reset, err := c.ResetSearchEngine(ctx, connect.NewRequest(&metav1.ResetSearchEngineRequest{}))
	if err != nil || reset.Msg.GetSettings().GetBackend() != "postgres" || reset.Msg.GetSettings().GetOrigin() != "config" {
		t.Fatalf("reset: %v %v", reset, err)
	}
	var pending int
	_ = f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM search_outbox`).Scan(&pending)
	if pending != 0 {
		t.Fatalf("outbox after going back to PostgreSQL: %d", pending)
	}
}
