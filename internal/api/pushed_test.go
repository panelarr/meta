// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/api"
	"github.com/panelarr/meta/internal/config"
	"github.com/panelarr/meta/internal/host"
	"github.com/panelarr/meta/internal/integrations"
	"github.com/panelarr/meta/internal/secret"
	"github.com/panelarr/meta/internal/store/storetest"
	"github.com/panelarr/meta/sdk/client"
	"github.com/panelarr/meta/sdk/plugintest"
)

// TestPushedProfileReachesThePlugin: a profile pushed by a host reaches the
// provider's plugin at once, is handed again to a new plugin process after a
// reconnect, can be read back without its password, and once removed the
// plugin falls back to meta's own assignment.
func TestPushedProfileReachesThePlugin(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Each dial is a new plugin process.
	var mu sync.Mutex
	var current *plugintest.Fake
	plugin := func() *plugintest.Fake {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "vpnprov", Remote: &config.Remote{URL: "x"}}}
	h := host.New(cfg, st, log, func(context.Context, config.Provider, *slog.Logger) (client.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		current = &plugintest.Fake{Provider: "vpnprov", UsesNetworkProfiles: true}
		return client.InProcess(current), nil
	})
	t.Cleanup(h.Close)
	// Wired as meta wires it.
	integ := integrations.New(st, secret.NewBox("", filepath.Join(t.TempDir(), "secret.key")), func() []integrations.ProviderInfo {
		var out []integrations.ProviderInfo
		for _, p := range h.List() {
			out = append(out, integrations.ProviderInfo{ID: p.ID(), Supported: p.UsesNetworkProfiles()})
		}
		return out
	})
	h.SetProfileSource(func(ctx context.Context, provider string) (*metadatav1.NetworkProfile, error) {
		np, _, err := integ.Effective(ctx, provider)
		return np, err
	})
	integ.OnChange = func() { go h.PushProfiles(ctx) }
	in := api.NewIntegrations(integ)

	p, _ := h.Get("vpnprov")
	if err := p.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	// What the plugin was handed last, waiting for it to arrive.
	last := func(want func(*metadatav1.NetworkProfile) bool) *metadatav1.NetworkProfile {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			got := plugin().Profiles()
			var np *metadatav1.NetworkProfile
			if len(got) > 0 {
				np = got[len(got)-1]
			}
			if len(got) > 0 && want(np) {
				return np
			}
			if time.Now().After(deadline) {
				t.Fatalf("plugin got %v", got)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// meta's own assignment first, so removal has something to fall back to.
	if _, err := in.PutProfile(ctx, connect.NewRequest(&metav1.PutProfileRequest{Profile: &metav1.NetworkProfile{Id: "local", Name: "Local"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := in.AssignProfile(ctx, connect.NewRequest(&metav1.AssignProfileRequest{ProviderId: "vpnprov", ProfileId: "local"})); err != nil {
		t.Fatal(err)
	}
	last(func(np *metadatav1.NetworkProfile) bool { return np.GetName() == "Local" })

	pushed := &metadatav1.NetworkProfile{
		Name:   "vpn-nl",
		Proxy:  &metadatav1.Proxy{Url: "socks5://gluetun:1080", Username: "me", Password: "s3cret"},
		DohUrl: "https://dns.example/dns-query",
	}
	before := time.Now().Add(-time.Second)
	if _, err := in.SetPushedProfile(ctx, connect.NewRequest(&metav1.SetPushedProfileRequest{ProviderId: "vpnprov", Profile: pushed})); err != nil {
		t.Fatal(err)
	}
	np := last(func(np *metadatav1.NetworkProfile) bool { return np.GetName() == "vpn-nl" })
	if np.GetProxy().GetPassword() != "s3cret" || np.GetDohUrl() != "https://dns.example/dns-query" {
		t.Fatalf("pushed to the plugin: %v", np)
	}

	// Read back for the host: everything but the password.
	list, err := in.ListAssignments(ctx, connect.NewRequest(&metav1.ListAssignmentsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	a := list.Msg.GetAssignments()[0]
	if !a.GetSupported() || a.GetEffective() != metav1.ProfileOrigin_PROFILE_ORIGIN_PUSHED || a.GetPushedProfileName() != "vpn-nl" ||
		a.GetPushedProfile().GetProxy().GetUrl() != "socks5://gluetun:1080" || a.GetPushedProfile().GetProxy().GetUsername() != "me" ||
		a.GetPushedProfile().GetDohUrl() != "https://dns.example/dns-query" || !a.GetPushedProxyPasswordSet() ||
		a.GetPushedAt().AsTime().Before(before) {
		t.Fatalf("read back: %v", a)
	}
	if a.GetPushedProfile().GetProxy().GetPassword() != "" || strings.Contains(list.Msg.String(), "s3cret") {
		t.Fatal("the read-back carries the password")
	}

	// A new plugin process gets the pushed profile when meta connects to it.
	h.Close()
	if err := p.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if np := last(func(np *metadatav1.NetworkProfile) bool { return np.GetName() == "vpn-nl" }); np.GetProxy().GetPassword() != "s3cret" {
		t.Fatalf("after reconnect: %v", np)
	}

	// Removed by the host: back to meta's own assignment.
	if _, err := in.SetPushedProfile(ctx, connect.NewRequest(&metav1.SetPushedProfileRequest{ProviderId: "vpnprov"})); err != nil {
		t.Fatal(err)
	}
	last(func(np *metadatav1.NetworkProfile) bool { return np.GetName() == "Local" })
	list, err = in.ListAssignments(ctx, connect.NewRequest(&metav1.ListAssignmentsRequest{}))
	if a := list.Msg.GetAssignments()[0]; err != nil || a.GetPushedProfile() != nil || a.GetPushedAt() != nil || a.GetPushedProxyPasswordSet() ||
		a.GetEffective() != metav1.ProfileOrigin_PROFILE_ORIGIN_ASSIGNED {
		t.Fatalf("after removal: %v %v", a, err)
	}
}
