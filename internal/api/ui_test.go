// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminui "github.com/panelarr/meta/admin"
	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
)

type reply struct {
	StatusCode int
	Header     http.Header
}

func do(t *testing.T, method, url, token, body string, hdr ...string) (reply, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, url, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return reply{StatusCode: res.StatusCode, Header: res.Header}, string(b)
}

func TestUIAndAPIRouting(t *testing.T) {
	t.Parallel()
	f := setup(t, "k")
	uiStatus := http.StatusOK
	if !adminui.Built() {
		uiStatus = http.StatusServiceUnavailable // "run make admin"
	}

	// The UI loads without credentials, under the CSP.
	for _, path := range []string{"/", "/catalogue", "/entries/anything", "/clients"} {
		res, body := do(t, http.MethodGet, f.srv.URL+path, "", "")
		if res.StatusCode != uiStatus || !strings.Contains(body, "meta admin") {
			t.Fatalf("%s: %d %q", path, res.StatusCode, body)
		}
		if res.Header.Get("Content-Security-Policy") != adminui.CSP {
			t.Fatalf("%s: CSP %q", path, res.Header.Get("Content-Security-Policy"))
		}
	}

	// API paths never get the UI: they need credentials...
	res, body := do(t, http.MethodGet, f.srv.URL+"/v1/whoami", "", "")
	if res.StatusCode != http.StatusUnauthorized || strings.Contains(body, "<") {
		t.Fatalf("whoami without a token: %d %q", res.StatusCode, body)
	}
	// ...and unknown API paths are JSON 404s.
	res, body = do(t, http.MethodGet, f.srv.URL+"/v1/no/such/thing", testAdminToken, "")
	var status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if res.StatusCode != http.StatusNotFound || json.Unmarshal([]byte(body), &status) != nil || status.Code != 5 {
		t.Fatalf("unknown API path: %d %q", res.StatusCode, body)
	}
	// gRPC/Connect service paths stay with the API.
	res, _ = do(t, http.MethodPost, f.srv.URL+"/panelarr.meta.v1.MetaClientsService/WhoAmI", "", "{}", "Content-Type", "application/json")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Connect path without a token: %d", res.StatusCode)
	}

	// The REST calls the UI makes: camelCase filter params, a POST without a body.
	res, body = do(t, http.MethodGet, f.srv.URL+"/v1/entries?filter.contentRatings=CONTENT_RATING_SAFE&filter.yearFrom=2015", testAdminToken, "")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Lantern Keeper") || strings.Contains(body, "Attack of the Giants") {
		t.Fatalf("browse with filters: %d %q", res.StatusCode, body)
	}
	created, err := f.as(testAdminToken).clients.CreateClient(context.Background(), connect.NewRequest(&metav1.CreateClientRequest{Name: "ui", Scopes: []string{"catalog:read"}}))
	if err != nil {
		t.Fatal(err)
	}
	res, body = do(t, http.MethodPost, f.srv.URL+"/v1/admin/clients/"+created.Msg.GetClient().GetId()+"/rotate", testAdminToken, "")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "mtk_") {
		t.Fatalf("rotate without a body: %d %q", res.StatusCode, body)
	}
	// A client key gets a JSON 403 for admin paths (the UI shows its message).
	res, body = do(t, http.MethodGet, f.srv.URL+"/v1/admin/providers", created.Msg.GetApiKey(), "")
	if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusForbidden {
		t.Fatalf("client key on admin path: %d %q", res.StatusCode, body)
	}
}
