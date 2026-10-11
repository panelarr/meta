// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/sdk/client"
	"github.com/panelarr/meta/sdk/conformance"
	"github.com/panelarr/meta/sdk/plugin"
	"github.com/panelarr/meta/sdk/plugintest"
)

func fake() *plugintest.Fake {
	f := &plugintest.Fake{Provider: "fake", SnapshotID: "snap-1"}
	f.Entries = []*metadatav1.ProviderEntry{
		f.Entry("1", "First Light", "Hajimari no Hikari"),
		f.Entry("2", "Second Wind"),
		f.Entry("3", "Third Act"),
	}
	f.Merges = []plugintest.Merge{{Merged: "9", Into: "2"}}
	return f
}

func remote(t *testing.T, p plugin.Plugin, key string) client.Client {
	t.Helper()
	srv := httptest.NewUnstartedServer(plugin.NewHandler(p, key))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	c := client.Remote(srv.URL, client.RemoteOptions{APIKey: key, HTTPClient: srv.Client()})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// buildExample builds the example plugin binary for managed-mode tests.
func buildExample(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "static-plugin")
	cmd := exec.Command("go", "build", "-o", bin, "../examples/static-plugin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build example plugin: %v\n%s", err, out)
	}
	return bin
}

func TestConformanceInProcess(t *testing.T) {
	rep := conformance.Run(t, client.InProcess(fake()), conformance.Options{
		Settings: map[string]any{"greeting": "hi"},
	})
	if rep.Events["upsert"] != 3 || rep.Events["merge"] != 1 || rep.Events["checkpoint"] == 0 {
		t.Fatalf("unexpected events: %v", rep.Events)
	}
}

func TestConformanceRemote(t *testing.T) {
	rep := conformance.Run(t, remote(t, fake(), "secret"), conformance.Options{})
	if rep.Events["upsert"] != 3 {
		t.Fatalf("unexpected events: %v", rep.Events)
	}
}

func TestConformanceRemoteGRPCProtocol(t *testing.T) {
	srv := httptest.NewUnstartedServer(plugin.NewHandler(fake(), ""))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	c := client.Remote(srv.URL, client.RemoteOptions{GRPC: true, HTTPClient: srv.Client()})
	conformance.Run(t, c, conformance.Options{})
}

func TestConformanceManaged(t *testing.T) {
	bin := buildExample(t)
	c, err := client.Managed(bin, client.ManagedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	rep := conformance.Run(t, c, conformance.Options{})
	if rep.Info.GetId() != "static" || rep.Events["upsert"] != 3 || rep.Events["merge"] != 1 {
		t.Fatalf("unexpected report: %v %v", rep.Info.GetId(), rep.Events)
	}
}

func TestStandaloneBinary(t *testing.T) {
	bin := buildExample(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Pick a free port up front.
	ln := httptest.NewServer(nil)
	addr := ln.Listener.Addr().String()
	ln.Close()
	cmd := exec.CommandContext(ctx, bin, "--listen", addr, "--api-key", "k")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	c := client.Remote("http://"+addr, client.RemoteOptions{APIKey: "k"})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := c.GetInfo(ctx); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("plugin did not come up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	conformance.Run(t, c, conformance.Options{})

	bad := client.Remote("http://"+addr, client.RemoteOptions{APIKey: "wrong"})
	if _, err := bad.GetInfo(ctx); plugin.ErrorCode(err) != plugin.Code(16) { // unauthenticated
		t.Fatalf("wrong key: got %v", err)
	}
}

func TestAPIKeyRequired(t *testing.T) {
	srv := httptest.NewServer(plugin.NewHandler(fake(), "secret"))
	defer srv.Close()
	c := client.Remote(srv.URL, client.RemoteOptions{})
	_, err := c.GetInfo(context.Background())
	var pe *plugin.Error
	if !errors.As(err, &pe) || pe.Code != plugin.Code(16) {
		t.Fatalf("want unauthenticated, got %v", err)
	}
}

type rateLimited struct{ plugin.Unimplemented }

func (rateLimited) Get(context.Context, *metadatav1.GetRequest) (*metadatav1.GetResponse, error) {
	return nil, plugin.ErrRateLimited(42*time.Second, "slow down")
}

func TestErrorsCrossTheWire(t *testing.T) {
	check := func(t *testing.T, c client.Client) {
		_, err := c.Get(context.Background(), &metadatav1.ExternalId{Provider: "x", Id: "1"})
		var pe *plugin.Error
		if !errors.As(err, &pe) {
			t.Fatalf("want *plugin.Error, got %T %v", err, err)
		}
		if pe.Code != plugin.CodeResourceExhausted || pe.RetryAfter != 42*time.Second || pe.Message != "slow down" {
			t.Fatalf("got %+v", pe)
		}
		_, err = c.Search(context.Background(), &metadatav1.SearchRequest{})
		if plugin.ErrorCode(err) != plugin.CodeUnimplemented {
			t.Fatalf("want unimplemented, got %v", err)
		}
	}
	t.Run("in-process", func(t *testing.T) { check(t, client.InProcess(rateLimited{})) })
	t.Run("remote", func(t *testing.T) { check(t, remote(t, rateLimited{}, "")) })
}

func TestSyncCancel(t *testing.T) {
	f := fake()
	f.SyncDelay = 50 * time.Millisecond
	for name, c := range map[string]client.Client{"in-process": client.InProcess(f), "remote": remote(t, f, "")} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			s, err := c.Sync(ctx, &metadatav1.SyncRequest{Kind: plugin.KindDump})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Recv(); err != nil { // started
				t.Fatal(err)
			}
			cancel()
			for {
				_, err := s.Recv()
				if err == nil {
					continue
				}
				if errors.Is(err, io.EOF) {
					t.Fatal("stream ended normally after cancel")
				}
				break
			}
			_ = s.Close()
		})
	}
}

func TestUnchangedSnapshot(t *testing.T) {
	c := client.InProcess(fake())
	s, err := c.Sync(context.Background(), &metadatav1.SyncRequest{Kind: plugin.KindDump, Cursor: "snap-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ev, err := s.Recv()
	if err != nil || !ev.GetStarted().GetUnchanged() {
		t.Fatalf("want unchanged start, got %v %v", ev, err)
	}
}
