// SPDX-License-Identifier: GPL-3.0-or-later

package host_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/sdk/client"
	"github.com/panelarr/meta/sdk/plugintest"
)

func TestProfilesReachPluginsThatUseThem(t *testing.T) {
	ctx := context.Background()
	uses := &plugintest.Fake{Provider: "uses", UsesNetworkProfiles: true}
	ignores := &plugintest.Fake{Provider: "ignores"}
	fakes := map[string]*plugintest.Fake{"uses": uses, "ignores": ignores}

	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{ID: "uses", Remote: &config.Remote{URL: "x"}},
		{ID: "ignores", Remote: &config.Remote{URL: "x"}},
	}
	h := host.New(cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func(_ context.Context, p config.Provider, _ *slog.Logger) (client.Client, error) {
		return client.InProcess(fakes[p.ID]), nil
	})
	var mu sync.Mutex
	current := &metadatav1.NetworkProfile{Name: "vpn", Proxy: &metadatav1.Proxy{Url: "http://gluetun:8888"}}
	h.SetProfileSource(func(_ context.Context, provider string) (*metadatav1.NetworkProfile, error) {
		mu.Lock()
		defer mu.Unlock()
		return current, nil
	})

	for _, id := range []string{"uses", "ignores"} {
		p, _ := h.Get(id)
		if err := p.Connect(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := uses.Profiles(); len(got) != 1 || got[0].GetName() != "vpn" {
		t.Fatalf("on connect: %v", got)
	}
	if p, _ := h.Get("uses"); !p.UsesNetworkProfiles() {
		t.Fatal("UsesNetworkProfiles")
	}

	// The integrations change: the new profile is pushed.
	mu.Lock()
	current = nil
	mu.Unlock()
	h.PushProfiles(ctx)
	if got := uses.Profiles(); len(got) != 2 || got[1] != nil {
		t.Fatalf("after change: %v", got)
	}
	if len(ignores.Profiles()) != 0 {
		t.Fatal("a plugin without network_profile got a profile")
	}
}

// stoppable is a plugin connection that stops answering when its plugin
// stops, as a gRPC connection to a stopped container does.
type stoppable struct {
	client.Client
	stopped *atomic.Bool
}

func (s stoppable) Health(ctx context.Context) (*metadatav1.HealthResponse, error) {
	if s.stopped.Load() {
		return nil, errors.New("dial tcp: lookup plugin: no such host")
	}
	return s.Client.Health(ctx)
}

// TestProbeNoticesAStoppedPlugin: a plugin that stops after meta connected
// is reported unavailable, by name, as soon as it is probed; it is
// reconnected once it is back.
func TestProbeNoticesAStoppedPlugin(t *testing.T) {
	ctx := context.Background()
	var stopped atomic.Bool
	dials := 0
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "gone", Remote: &config.Remote{URL: "x"}}}
	h := host.New(cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, config.Provider, *slog.Logger) (client.Client, error) {
		if stopped.Load() {
			return nil, errors.New("connection refused")
		}
		dials++
		return stoppable{client.InProcess(&plugintest.Fake{Provider: "gone"}), &stopped}, nil
	})
	p, _ := h.Get("gone")
	if err := p.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := p.Health(); s != metadatav1.HealthStatus_HEALTH_STATUS_OK {
		t.Fatalf("health %v", s)
	}

	stopped.Store(true)
	if err := p.Connect(ctx); err != nil {
		t.Fatalf("Connect only checks there is a connection: %v", err)
	}
	err := p.Probe(ctx)
	var unavailable *host.UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Provider != "gone" || !errors.Is(err, host.ErrNotConnected) {
		t.Fatalf("Probe: %v", err)
	}
	if s, msg := p.Health(); s != metadatav1.HealthStatus_HEALTH_STATUS_DOWN || msg == "" {
		t.Fatalf("health %v %q", s, msg)
	}
	if err := p.Connect(ctx); !errors.Is(err, host.ErrNotConnected) {
		t.Fatalf("the connection was kept: %v", err)
	}

	stopped.Store(false)
	if err := p.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if dials != 2 {
		t.Fatalf("dials %d, want a reconnect", dials)
	}
}

// TestProbeIgnoresACallerThatGaveUp: a probe whose caller went away doesn't
// mark the plugin down.
func TestProbeIgnoresACallerThatGaveUp(t *testing.T) {
	var stopped atomic.Bool
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "fine", Remote: &config.Remote{URL: "x"}}}
	h := host.New(cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, config.Provider, *slog.Logger) (client.Client, error) {
		return stoppable{client.InProcess(&plugintest.Fake{Provider: "fine"}), &stopped}, nil
	})
	p, _ := h.Get("fine")
	if err := p.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped.Store(true) // the call fails, but because ctx ended
	if err := p.Probe(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe: %v", err)
	}
	if s, _ := p.Health(); s != metadatav1.HealthStatus_HEALTH_STATUS_OK {
		t.Fatalf("health %v", s)
	}
}
