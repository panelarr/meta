// SPDX-License-Identifier: GPL-3.0-or-later

package auth

import "testing"

func TestRequiredScope(t *testing.T) {
	cases := map[string]string{
		"/panelarr.meta.v1.MetaService/Browse":                 ScopeCatalogRead,
		"/panelarr.meta.v1.MetaService/LookupExternal":         ScopeCatalogRead,
		"/panelarr.meta.v1.MetaClientsService/WhoAmI":          "",
		"/panelarr.meta.v1.MetaClientsService/CreateClient":    ScopeAdmin,
		"/panelarr.meta.v1.MetaAdminService/RunSync":           ScopeAdmin,
		"/panelarr.meta.v1.MetaIntegrationsService/PutProfile": ScopeAdmin,
		"/some.Unknown/Thing":                                  ScopeAdmin,
	}
	for proc, want := range cases {
		if got := RequiredScope(proc); got != want {
			t.Errorf("%s: %q, want %q", proc, got, want)
		}
	}
}

func TestAllowsAndBearer(t *testing.T) {
	admin := Principal{Scopes: []string{ScopeAdmin}}
	reader := Principal{Scopes: []string{ScopeCatalogRead}}
	if !admin.Allows(ScopeCatalogRead) || !reader.Allows(ScopeCatalogRead) || reader.Allows(ScopeAdmin) {
		t.Fatal("Allows")
	}
	for in, want := range map[string]string{"Bearer abc": "abc", "bearer  abc ": "abc", "Basic abc": "", "": "", "Bearer": ""} {
		if got := BearerToken(in); got != want {
			t.Errorf("BearerToken(%q) = %q", in, got)
		}
	}
	tok, err := NewToken(ClientKeyPrefix)
	if err != nil || len(tok) != len(ClientKeyPrefix)+43 {
		t.Fatalf("token %q %v", tok, err)
	}
}
