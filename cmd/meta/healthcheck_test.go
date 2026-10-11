// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer bad.Close()
	if code := runHealthcheck([]string{"--url", ok.URL}, io.Discard); code != 0 {
		t.Fatalf("healthy: %d", code)
	}
	if code := runHealthcheck([]string{"--url", bad.URL}, io.Discard); code != 1 {
		t.Fatalf("unhealthy: %d", code)
	}
	t.Setenv("META_LISTEN", "0.0.0.0:9999")
	if got := healthURL(); got != "http://127.0.0.1:9999/healthz" {
		t.Fatalf("url: %s", got)
	}
}
