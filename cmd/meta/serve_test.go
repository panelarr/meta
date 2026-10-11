// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/panelarr/meta/internal/store/storetest"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func buildPlugin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "static-plugin")
	if out, err := exec.Command("go", "build", "-o", bin, "../../sdk/examples/static-plugin").CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}
	return bin
}

type client struct {
	t    *testing.T
	base string
	key  string
}

func (c client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, c.base+path, r)
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil && res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	return res.StatusCode
}

// TestServeEndToEnd runs meta with a managed plugin, triggers a sync over
// the REST API and follows it to the end.
func TestServeEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin and needs PostgreSQL")
	}
	plugin := buildPlugin(t)
	addr := freeAddr(t)
	cfgPath := filepath.Join(t.TempDir(), "meta.yaml")
	cfg := fmt.Sprintf(`
listen: %q
database_url: %q
api_keys: [test-key]
log: {level: warn}
providers:
  - id: static
    managed:
      path: %q
`, addr, storetest.URL(t), plugin)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"serve", "--config", cfgPath}, io.Discard) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	}()

	c := client{t: t, base: "http://" + addr, key: "test-key"}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if res, err := http.Get(c.base + "/readyz"); err == nil { //nolint:noctx // test
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("meta did not become ready")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if code := (client{t: t, base: c.base}).do("GET", "/v1/admin/providers", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("no key: status %d", code)
	}
	if code := c.do("GET", "/openapi.yaml", nil, nil); code != http.StatusOK {
		t.Fatalf("openapi: %d", code)
	}

	var started struct {
		Run struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"run"`
	}
	for {
		code := c.do("POST", "/v1/admin/providers/static/sync", map[string]any{"kind": "dump"}, &started)
		if code == http.StatusOK {
			break
		}
		// The plugin may still be starting.
		if time.Now().After(deadline) {
			t.Fatalf("run sync: status %d", code)
		}
		time.Sleep(100 * time.Millisecond)
	}

	var got struct {
		Run struct {
			Status  string `json:"status"`
			Upserts string `json:"upserts"`
			Merges  string `json:"merges"`
			Error   string `json:"error"`
		} `json:"run"`
	}
	for {
		if code := c.do("GET", "/v1/admin/sync-runs/"+started.Run.ID, nil, &got); code != http.StatusOK {
			t.Fatalf("get run: %d", code)
		}
		if got.Run.Status != "SYNC_RUN_STATUS_RUNNING" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sync did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got.Run.Status != "SYNC_RUN_STATUS_SUCCEEDED" || got.Run.Upserts != "3" || got.Run.Merges != "1" {
		t.Fatalf("run: %+v", got.Run)
	}

	var providers struct {
		Providers []struct {
			ID          string `json:"id"`
			Mode        string `json:"mode"`
			Health      string `json:"health"`
			RecordCount string `json:"recordCount"`
		} `json:"providers"`
	}
	c.do("GET", "/v1/admin/providers", nil, &providers)
	if len(providers.Providers) != 1 || providers.Providers[0].Health != "HEALTH_STATUS_OK" || providers.Providers[0].RecordCount != "3" || providers.Providers[0].Mode != "managed" {
		t.Fatalf("providers: %+v", providers)
	}

	// A second run while none is running works; a bad run id is a 400.
	if code := c.do("GET", "/v1/admin/sync-runs/nope", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", code)
	}
	if code := c.do("POST", "/v1/admin/providers/unknown/sync", map[string]any{}, nil); code != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", code)
	}
}
