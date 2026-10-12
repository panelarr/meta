// SPDX-License-Identifier: Apache-2.0

// Package settings helps plugins describe and validate their settings with
// JSON Schema (draft 2020-12), the format meta uses to render settings forms.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
//
// The order properties are written in is lost on the way to a host (a
// protobuf Struct and JSON maps sort keys), so Parse stamps it on every
// property, at every depth, as "x-order": 1, 2, 3, … in the order written.
// Forms show fields in that order. A property that already has "x-order" or
// "propertyOrder" keeps it.
func Parse(doc string) (*Schema, error) {
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	root, err := decodeOrdered(dec)
	if err != nil {
		return nil, fmt.Errorf("settings: schema is not JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("settings: schema is not JSON: data after the document")
	}
	m, ok := stampOrder(root).(map[string]any)
	if !ok {
		return nil, errors.New("settings: schema is not a JSON object")
	}
	return compile(m)
}

// ordered is a JSON object with its keys in the order they were written.
type ordered struct {
	keys   []string
	values map[string]any
}

// decodeOrdered reads one JSON value, keeping the key order of objects.
func decodeOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &ordered{values: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				v, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				if _, dup := o.values[key]; !dup {
					o.keys = append(o.keys, key)
				}
				o.values[key] = v
			}
			_, err := dec.Token() // }
			return o, err
		case '[':
			list := []any{}
			for dec.More() {
				v, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token() // ]
			return list, err
		}
		return nil, fmt.Errorf("unexpected %v", t)
	case json.Number:
		// Numbers become float64, as encoding/json decodes them.
		return t.Float64()
	default:
		return t, nil
	}
}

// stampOrder turns decoded values into plain maps and lists, and sets
// "x-order" on the members of every "properties" object from their written
// order.
func stampOrder(v any) any {
	switch t := v.(type) {
	case *ordered:
		out := make(map[string]any, len(t.values))
		for _, k := range t.keys {
			out[k] = stampOrder(t.values[k])
		}
		props, ok := t.values["properties"].(*ordered)
		if !ok {
			return out
		}
		stamped, _ := out["properties"].(map[string]any)
		for i, name := range props.keys {
			prop, ok := stamped[name].(map[string]any)
			if !ok {
				continue
			}
			_, hasX := prop["x-order"]
			_, hasP := prop["propertyOrder"]
			if !hasX && !hasP {
				prop["x-order"] = float64(i + 1)
			}
		}
		return out
	case []any:
		for i, e := range t {
			t[i] = stampOrder(e)
		}
		return t
	default:
		return v
	}
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
