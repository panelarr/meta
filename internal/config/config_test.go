// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "meta.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFileAndEnv(t *testing.T) {
	t.Setenv("MB_KEY", "s3cret")
	t.Setenv("META_API_KEYS", "a, b")
	p := write(t, `
database_url: postgres://meta@db/meta
providers:
  - id: mangabaka
    remote:
      url: http://meta-mangabaka:8903
      api_key: ${MB_KEY}
    settings:
      dump_variant: ${MB_VARIANT:-default}
    schedules:
      dump: "0 3 * * *"
merge:
  priority:
    credits: [anilist, mangabaka]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8901" {
		t.Errorf("listen default: %q", cfg.Listen)
	}
	pr := cfg.Providers[0]
	if pr.Remote.APIKey != "s3cret" || pr.Settings["dump_variant"] != "default" || pr.Mode() != "remote" {
		t.Errorf("provider: %+v %+v", pr, pr.Remote)
	}
	if strings.Join(cfg.APIKeys, "|") != "a|b" {
		t.Errorf("api keys: %v", cfg.APIKeys)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]string{
		"database_url is required":    `listen: ":1"`,
		"either managed or remote":    "database_url: x\nproviders: [{id: a, managed: {path: /p}, remote: {url: http://x}}]",
		"set managed.path or remote":  "database_url: x\nproviders: [{id: a}]",
		"must be lower case":          "database_url: x\nproviders: [{id: Bad, remote: {url: http://x}}]",
		"listed twice":                "database_url: x\nproviders: [{id: a, remote: {url: http://x}}, {id: a, remote: {url: http://y}}]",
		"schedule for":                "database_url: x\nproviders: [{id: a, remote: {url: http://x}, schedules: {dump: 'nonsense'}}]",
		"search.backend":              "database_url: x\nsearch: {backend: typesense}",
		"field unknown_key not found": "database_url: x\nunknown_key: 1",
		"lists a provider twice":      "database_url: x\nmerge: {priority: {titles: [a, b, a]}}",
	}
	for want, body := range cases {
		_, err := Load(write(t, body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v", want, err)
		}
	}
}

func TestEmptyFileAndEnvOnly(t *testing.T) {
	t.Setenv("META_DATABASE_URL", "postgres://x")
	if _, err := Load(write(t, "")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("META_CONFIG", "")
	cfg, err := Load("")
	if err != nil || cfg.DatabaseURL != "postgres://x" || len(cfg.Providers) != 0 {
		t.Fatalf("%+v %v", cfg, err)
	}
}
