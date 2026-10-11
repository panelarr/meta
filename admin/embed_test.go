// SPDX-License-Identifier: GPL-3.0-or-later

package adminui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesTheApp(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><title>meta admin</title>")},
		"favicon.svg":         {Data: []byte("<svg/>")},
		"assets/index-ab1.js": {Data: []byte("console.log(1)")},
		".keep":               {Data: nil},
	})

	for _, path := range []string{"/", "/catalogue", "/entries/x", "/providers?provider=a", "/.keep"} {
		rec := get(h, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "meta admin") {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: cache %q", path, rec.Header().Get("Cache-Control"))
		}
	}
	rec := get(h, "/assets/index-ab1.js")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("asset type: %q", ct)
	}
	for k, want := range map[string]string{
		"Content-Security-Policy": CSP,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s: %q", k, got)
		}
	}
	// Paths that climb out are refused by the file server.
	if rec := get(h, "/../../etc/passwd"); rec.Code != http.StatusBadRequest {
		t.Fatalf("path traversal: %d %q", rec.Code, rec.Body.String())
	}
}

func TestNotBuilt(t *testing.T) {
	rec := get(handler(fstest.MapFS{".keep": {}}), "/")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make admin") {
		t.Fatalf("not built: %d %q", rec.Code, rec.Body.String())
	}
}

func TestCSPAllowsCoversOnly(t *testing.T) {
	for _, want := range []string{"script-src 'self'", "style-src 'self'", "img-src 'self' data: blob: https:", "frame-ancestors 'none'"} {
		if !strings.Contains(CSP, want) {
			t.Errorf("CSP lacks %q", want)
		}
	}
	if strings.Contains(CSP, "unsafe-") {
		t.Error("CSP allows unsafe sources")
	}
}
