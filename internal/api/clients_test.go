// SPDX-License-Identifier: GPL-3.0-or-later

package api_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	metav1 "github.com/panelarr/meta/gen/panelarr/meta/v1"
	"github.com/panelarr/meta/gen/panelarr/meta/v1/metav1connect"
	"github.com/panelarr/meta/internal/auth"
	"github.com/panelarr/meta/internal/store/storetest"
)

type bearer string

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if b != "" {
			req.Header().Set("Authorization", "Bearer "+string(b))
		}
		return next(ctx, req)
	}
}
func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (b bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

type clientsFor struct {
	integrations metav1connect.MetaIntegrationsServiceClient
	meta         metav1connect.MetaServiceClient
	admin        metav1connect.MetaAdminServiceClient
	clients      metav1connect.MetaClientsServiceClient
	jobs         metav1connect.MetaJobsServiceClient
}

func (f *fixture) as(token string) clientsFor {
	opt := connect.WithInterceptors(bearer(token))
	hc := f.srv.Client()
	return clientsFor{
		integrations: metav1connect.NewMetaIntegrationsServiceClient(hc, f.srv.URL, opt),
		meta:         metav1connect.NewMetaServiceClient(hc, f.srv.URL, opt),
		admin:        metav1connect.NewMetaAdminServiceClient(hc, f.srv.URL, opt),
		clients:      metav1connect.NewMetaClientsServiceClient(hc, f.srv.URL, opt),
		jobs:         metav1connect.NewMetaJobsServiceClient(hc, f.srv.URL, opt),
	}
}

func TestClientKeysAndScopes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "config-key")
	admin := f.as(testAdminToken)

	who, err := admin.clients.WhoAmI(ctx, connect.NewRequest(&metav1.WhoAmIRequest{}))
	if err != nil || who.Msg.GetKind() != metav1.PrincipalKind_PRINCIPAL_KIND_ADMIN {
		t.Fatalf("admin whoami: %v %v", who, err)
	}

	created, err := admin.clients.CreateClient(ctx, connect.NewRequest(&metav1.CreateClientRequest{Name: " Library server ", Scopes: []string{"catalog:read"}}))
	if err != nil {
		t.Fatal(err)
	}
	key := created.Msg.GetApiKey()
	c := created.Msg.GetClient()
	if !strings.HasPrefix(key, auth.ClientKeyPrefix) || c.GetKeyPrefix() != key[:auth.KeyPrefixLen] || c.GetName() != "Library server" {
		t.Fatalf("created: %v key %q", c, key)
	}

	// The read key reads the catalogue...
	reader := f.as(key)
	if _, err := reader.meta.Browse(ctx, connect.NewRequest(&metav1.BrowseRequest{})); err != nil {
		t.Fatalf("read key browse: %v", err)
	}
	who, err = reader.clients.WhoAmI(ctx, connect.NewRequest(&metav1.WhoAmIRequest{}))
	if err != nil || who.Msg.GetKind() != metav1.PrincipalKind_PRINCIPAL_KIND_CLIENT || who.Msg.GetClientId() != c.GetId() {
		t.Fatalf("client whoami: %v %v", who, err)
	}
	// ...and can't call admin APIs.
	if _, err := reader.admin.ListProviders(ctx, connect.NewRequest(&metav1.ListProvidersRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("read key ListProviders: %v", err)
	}
	if _, err := reader.admin.RunSync(ctx, connect.NewRequest(&metav1.RunSyncRequest{Provider: "fake"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("read key RunSync: %v", err)
	}
	if _, err := reader.clients.CreateClient(ctx, connect.NewRequest(&metav1.CreateClientRequest{Name: "x", Scopes: []string{"admin"}})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("read key CreateClient: %v", err)
	}
	// Over REST too: 403 for admin paths, 200 for the catalogue.
	restGet := func(path, token string) int {
		req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := restGet("/v1/admin/clients", key); code != http.StatusForbidden {
		t.Fatalf("REST admin with read key: %d", code)
	}
	if code := restGet("/v1/entries", key); code != http.StatusOK {
		t.Fatalf("REST browse with read key: %d", code)
	}
	if code := restGet("/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", key); code != http.StatusForbidden {
		t.Fatalf("reflection with read key: %d", code)
	}

	// The list never shows keys, and records last use.
	list, err := admin.clients.ListClients(ctx, connect.NewRequest(&metav1.ListClientsRequest{}))
	if err != nil || len(list.Msg.GetClients()) != 1 || list.Msg.GetClients()[0].GetLastUsedAt() == nil {
		t.Fatalf("list: %v %v", list, err)
	}

	// Upgrading the scopes keeps the key and grants admin.
	if _, err := admin.clients.UpdateClient(ctx, connect.NewRequest(&metav1.UpdateClientRequest{ClientId: c.GetId(), Scopes: []string{"admin"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.admin.ListProviders(ctx, connect.NewRequest(&metav1.ListProvidersRequest{})); err != nil {
		t.Fatalf("after upgrade: %v", err)
	}

	// Rotating stops the old key at once.
	rotated, err := admin.clients.RotateClientKey(ctx, connect.NewRequest(&metav1.RotateClientKeyRequest{ClientId: c.GetId()}))
	if err != nil || rotated.Msg.GetApiKey() == key || rotated.Msg.GetClient().GetRotatedAt() == nil {
		t.Fatalf("rotate: %v %v", rotated, err)
	}
	if _, err := reader.meta.Browse(ctx, connect.NewRequest(&metav1.BrowseRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("old key after rotate: %v", err)
	}
	fresh := f.as(rotated.Msg.GetApiKey())
	if _, err := fresh.meta.Browse(ctx, connect.NewRequest(&metav1.BrowseRequest{})); err != nil {
		t.Fatalf("new key: %v", err)
	}

	// Deleting stops the key.
	if _, err := admin.clients.DeleteClient(ctx, connect.NewRequest(&metav1.DeleteClientRequest{ClientId: c.GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.meta.Browse(ctx, connect.NewRequest(&metav1.BrowseRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("deleted key: %v", err)
	}
	if _, err := admin.clients.DeleteClient(ctx, connect.NewRequest(&metav1.DeleteClientRequest{ClientId: c.GetId()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("delete twice: %v", err)
	}

	// The deprecated config key still has full access.
	cfgKey := f.as("config-key")
	who, err = cfgKey.clients.WhoAmI(ctx, connect.NewRequest(&metav1.WhoAmIRequest{}))
	if err != nil || who.Msg.GetKind() != metav1.PrincipalKind_PRINCIPAL_KIND_CONFIG_KEY {
		t.Fatalf("config key whoami: %v %v", who, err)
	}
	if _, err := cfgKey.admin.ListProviders(ctx, connect.NewRequest(&metav1.ListProvidersRequest{})); err != nil {
		t.Fatalf("config key admin: %v", err)
	}

	// No or wrong credentials.
	for _, tok := range []string{"", "mtk_nope", "nope"} {
		if _, err := f.as(tok).meta.Browse(ctx, connect.NewRequest(&metav1.BrowseRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("token %q: %v", tok, err)
		}
	}
}

func TestClientValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t, "k")
	admin := f.as(testAdminToken)
	bad := []*metav1.CreateClientRequest{
		{Name: "", Scopes: []string{"admin"}},
		{Name: strings.Repeat("x", 101), Scopes: []string{"admin"}},
		{Name: "ok"},
		{Name: "ok", Scopes: []string{"sources:read"}},
	}
	for _, r := range bad {
		if _, err := admin.clients.CreateClient(ctx, connect.NewRequest(r)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%v: %v", r, err)
		}
	}
	res, err := admin.clients.CreateClient(ctx, connect.NewRequest(&metav1.CreateClientRequest{Name: "dup", Scopes: []string{"admin", "catalog:read", "admin"}}))
	if err != nil || strings.Join(res.Msg.GetClient().GetScopes(), ",") != "catalog:read,admin" {
		t.Fatalf("scopes deduped and ordered: %v %v", res, err)
	}
	if _, err := admin.clients.UpdateClient(ctx, connect.NewRequest(&metav1.UpdateClientRequest{ClientId: "nope", Name: "x"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("update missing: %v", err)
	}
}

func TestGeneratedAdminToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := storetest.New(t)
	var out bytes.Buffer
	a, err := auth.New(ctx, st, auth.Options{Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out.String())
	var token string
	for _, w := range fields {
		if strings.HasPrefix(w, auth.AdminTokenPrefix) {
			token = w
		}
	}
	if token == "" {
		t.Fatalf("no token printed: %q", out.String())
	}
	if p, err := a.Authenticate(ctx, token); err != nil || p.Kind != auth.KindAdmin {
		t.Fatalf("generated token: %v %v", p, err)
	}
	// A restart reuses the stored hash and prints nothing.
	out.Reset()
	b, err := auth.New(ctx, st, auth.Options{Out: &out})
	if err != nil || out.Len() != 0 {
		t.Fatalf("second start printed %q (%v)", out.String(), err)
	}
	if _, err := b.Authenticate(ctx, token); err != nil {
		t.Fatalf("stored token: %v", err)
	}
	// META_ADMIN_TOKEN wins.
	c, err := auth.New(ctx, st, auth.Options{AdminToken: "mta_mine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Authenticate(ctx, token); err == nil {
		t.Fatal("stored token still works while META_ADMIN_TOKEN is set")
	}
	if p, err := c.Authenticate(ctx, "mta_mine"); err != nil || p.Kind != auth.KindAdmin {
		t.Fatalf("env token: %v %v", p, err)
	}
}
