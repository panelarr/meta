// SPDX-License-Identifier: Apache-2.0

// Package settings helps plugins describe and validate their settings with
// JSON Schema (draft 2020-12), the format meta uses to render settings forms.
package settings

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/types/known/structpb"
)

// Schema is a compiled settings schema.
type Schema struct {
	doc      map[string]any
	compiled *jsonschema.Schema
}

// Parse compiles a JSON Schema document given as JSON text.
func Parse(doc string) (*Schema, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		return nil, fmt.Errorf("settings: schema is not JSON: %w", err)
	}
	return compile(m)
}

// FromStruct compiles a JSON Schema document given as a protobuf Struct, as
// returned by GetSettingsSchema.
func FromStruct(s *structpb.Struct) (*Schema, error) {
	return compile(s.AsMap())
}

func compile(m map[string]any) (*Schema, error) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("settings.json", m); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	compiled, err := c.Compile("settings.json")
	if err != nil {
		return nil, fmt.Errorf("settings: invalid schema: %w", err)
	}
	return &Schema{doc: m, compiled: compiled}, nil
}

// Struct returns the schema document as a protobuf Struct.
func (s *Schema) Struct() *structpb.Struct {
	st, err := structpb.NewStruct(s.doc)
	if err != nil {
		// The document came from JSON or a Struct, so it always converts.
		panic(err)
	}
	return st
}

// Validate checks values against the schema.
func (s *Schema) Validate(values map[string]any) error {
	if values == nil {
		values = map[string]any{}
	}
	// Round-trip through JSON so numbers and nested values have the types
	// the validator expects.
	raw, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if err := s.compiled.Validate(inst); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	return nil
}

// WithDefaults returns values with the schema's top-level property defaults
// filled in where a value is missing.
func (s *Schema) WithDefaults(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for k, v := range values {
		out[k] = v
	}
	props, _ := s.doc["properties"].(map[string]any)
	for name, p := range props {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if _, set := out[name]; set {
			continue
		}
		if def, ok := pm["default"]; ok {
			out[name] = def
		}
	}
	return out
}
