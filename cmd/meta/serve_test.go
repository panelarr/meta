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
	metricsAddr := freeAddr(t)
	cfgPath := filepath.Join(t.TempDir(), "meta.yaml")
	cfg := fmt.Sprintf(`
listen: %q
database_url: %q
api_keys: [test-key]
log: {level: warn}
metrics: {enabled: true, listen: %q}
ingest: {stale_after: 1ms}
providers:
  - id: static
    managed:
      path: %q
`, addr, storetest.URL(t), metricsAddr, plugin)
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

	// Metrics on their own port, without a key.
	mres, err := http.Get("http://" + metricsAddr + "/metrics") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	mbody, _ := io.ReadAll(mres.Body)
	mres.Body.Close()
	if !bytes.Contains(mbody, []byte(`meta_sync_runs_total{kind="dump",provider="static",status="succeeded"} 1`)) {
		t.Fatalf("metrics:\n%s", mbody)
	}
	if code := c.do("GET", "/metrics", nil, nil); code != http.StatusNotFound && code != http.StatusUnauthorized && code != http.StatusMethodNotAllowed {
		// Not on the API port when a metrics address is set.
		t.Fatalf("metrics on the API port: %d", code)
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

	// Jobs: the provider's sync, and the rebuild the fresh database's
	// migrations queued, which ran at startup.
	var jobList struct {
		Jobs []struct {
			ID      string `json:"id"`
			LastRun struct {
				Status  string `json:"status"`
				Trigger string `json:"trigger"`
			} `json:"lastRun"`
		} `json:"jobs"`
	}
	waitJob := func(path string) map[string]any {
		t.Helper()
		for {
			var run struct {
				Run struct {
					Summary map[string]any `json:"summary"`
				} `json:"run"`
			}
			if code := c.do("GET", path, nil, &run); code != http.StatusOK {
				t.Fatalf("GET %s: %d", path, code)
			}
			if run.Run.Summary["status"] != "JOB_RUN_STATUS_RUNNING" {
				return run.Run.Summary
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not finish", path)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	for {
		c.do("GET", "/v1/admin/jobs", nil, &jobList)
		ids := map[string]string{}
		for _, j := range jobList.Jobs {
			ids[j.ID] = j.LastRun.Trigger + " " + j.LastRun.Status
		}
		if ids["rebuild-entries"] == "JOB_TRIGGER_STARTUP JOB_RUN_STATUS_SUCCEEDED" {
			if _, ok := ids["sync.static.dump"]; !ok {
				t.Fatalf("jobs: %v", ids)
			}
			if _, ok := ids["stale-refresh"]; !ok {
				t.Fatalf("jobs: %v", ids)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("startup rebuild: %v", ids)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var refresh struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if code := c.do("POST", "/v1/admin/jobs/stale-refresh:run", map[string]any{"params": map[string]string{"limit": "2"}}, &refresh); code != http.StatusOK {
		t.Fatalf("run stale refresh: %d", code)
	}
	sum := waitJob("/v1/admin/job-runs/" + refresh.Run.ID)
	counts, _ := sum["counts"].(map[string]any)
	if sum["status"] != "JOB_RUN_STATUS_SUCCEEDED" || counts["entries"] != "2" || sum["triggeredBy"] != "api_keys setting" {
		t.Fatalf("stale refresh: %v", sum)
	}
	mres, err = http.Get("http://" + metricsAddr + "/metrics") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	mbody, _ = io.ReadAll(mres.Body)
	mres.Body.Close()
	for _, line := range []string{
		`meta_job_runs_total{job="stale-refresh",status="succeeded",trigger="manual"} 1`,
		`meta_job_runs_total{job="sync.static.dump",status="succeeded",trigger="manual"} 1`,
		`meta_job_runs_total{job="rebuild-entries",status="succeeded",trigger="startup"} 1`,
	} {
		if !bytes.Contains(mbody, []byte(line)) {
			t.Fatalf("metrics lack %s:\n%s", line, mbody)
		}
	}

	// A second run while none is running works; a bad run id is a 400.
	if code := c.do("GET", "/v1/admin/sync-runs/nope", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", code)
	}
	if code := c.do("POST", "/v1/admin/providers/unknown/sync", map[string]any{}, nil); code != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", code)
	}
}
