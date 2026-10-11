// SPDX-License-Identifier: Apache-2.0

package settings_test

import (
	"testing"

	"github.com/panelarr/meta/sdk/settings"
)

const doc = `{
  "type": "object",
  "properties": {
    "variant": {"type": "string", "enum": ["default", "full"], "default": "default"},
    "batch": {"type": "integer", "minimum": 1, "default": 500}
  },
  "additionalProperties": false
}`

func TestValidateAndDefaults(t *testing.T) {
	s, err := settings.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(map[string]any{"variant": "full", "batch": float64(10)}); err != nil {
		t.Fatalf("valid values rejected: %v", err)
	}
	for _, bad := range []map[string]any{
		{"variant": "huge"},
		{"batch": float64(0)},
		{"unknown": true},
	} {
		if err := s.Validate(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	got := s.WithDefaults(map[string]any{"batch": float64(3)})
	if got["variant"] != "default" || got["batch"] != float64(3) {
		t.Fatalf("defaults: %v", got)
	}
	if _, err := settings.FromStruct(s.Struct()); err != nil {
		t.Fatalf("round trip: %v", err)
	}
}

func TestInvalidSchema(t *testing.T) {
	if _, err := settings.Parse(`{"type": 12}`); err == nil {
		t.Fatal("invalid schema accepted")
	}
	if _, err := settings.Parse(`not json`); err == nil {
		t.Fatal("non-JSON accepted")
	}
}
