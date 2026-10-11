// SPDX-License-Identifier: GPL-3.0-or-later

package host_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
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
