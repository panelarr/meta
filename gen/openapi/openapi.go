// SPDX-License-Identifier: Apache-2.0

// Package openapi embeds the OpenAPI document generated from the meta API
// protos.
package openapi

import _ "embed"

// Meta is the OpenAPI 3.1 document of the meta API (YAML).
//
//go:embed meta.openapi.yaml
var Meta []byte
