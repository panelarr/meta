// SPDX-License-Identifier: Apache-2.0

// Package plugin is the Go SDK for writing metadata plugins for meta.
//
// A plugin implements [Plugin] and calls [Main] (or [Serve]) from its main
// function. The same binary then works in both run modes:
//
//   - managed: meta starts the binary as a child process and talks to it over
//     gRPC on a local socket (HashiCorp go-plugin handshake);
//   - standalone: the binary is started with --listen and serves gRPC,
//     Connect and HTTP/JSON on that address, and meta connects to it by URL.
//
// Embed [Unimplemented] to get "not supported" answers for the calls a plugin
// does not need.
package plugin

import (
	"context"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
)

// Protocol is the protocol package plugins built with this SDK speak.
const Protocol = "panelarr.metadata.v1"

// Well-known ingestion kinds. The list is open.
const (
	KindLookup = "lookup"
	KindScrape = "scrape"
	KindDump   = "dump"
	KindDelta  = "delta"
)

// Plugin is what a metadata plugin implements. The methods mirror the
// MetadataPluginService RPCs; see the proto for their contracts.
//
// Return an [*Error] (see [Errorf] and the Err* helpers) to control the
// status code the host sees. Any other error is reported as an internal
// error.
type Plugin interface {
	GetInfo(ctx context.Context, req *metadatav1.GetInfoRequest) (*metadatav1.GetInfoResponse, error)
	Search(ctx context.Context, req *metadatav1.SearchRequest) (*metadatav1.SearchResponse, error)
	Get(ctx context.Context, req *metadatav1.GetRequest) (*metadatav1.GetResponse, error)
	// Sync streams events through send until the sync is done or ctx is
	// cancelled. Use [NewSyncWriter] for a convenient way to send them.
	Sync(ctx context.Context, req *metadatav1.SyncRequest, send SendFunc) error
	GetSettingsSchema(ctx context.Context, req *metadatav1.GetSettingsSchemaRequest) (*metadatav1.GetSettingsSchemaResponse, error)
	GetSettings(ctx context.Context, req *metadatav1.GetSettingsRequest) (*metadatav1.GetSettingsResponse, error)
	SetSettings(ctx context.Context, req *metadatav1.SetSettingsRequest) (*metadatav1.SetSettingsResponse, error)
	Health(ctx context.Context, req *metadatav1.HealthRequest) (*metadatav1.HealthResponse, error)
}

// SendFunc sends one sync event to the host. It returns an error when the
// host has gone away; the plugin should then stop.
type SendFunc func(*metadatav1.SyncResponse) error

// Unimplemented answers every call with CodeUnimplemented, except Health
// (always OK) and the settings calls (no settings). Embed it in a plugin and
// override what the plugin supports.
type Unimplemented struct{}

var _ Plugin = Unimplemented{}

func (Unimplemented) GetInfo(context.Context, *metadatav1.GetInfoRequest) (*metadatav1.GetInfoResponse, error) {
	return nil, ErrUnimplemented("GetInfo")
}

func (Unimplemented) Search(context.Context, *metadatav1.SearchRequest) (*metadatav1.SearchResponse, error) {
	return nil, ErrUnimplemented("Search")
}

func (Unimplemented) Get(context.Context, *metadatav1.GetRequest) (*metadatav1.GetResponse, error) {
	return nil, ErrUnimplemented("Get")
}

func (Unimplemented) Sync(context.Context, *metadatav1.SyncRequest, SendFunc) error {
	return ErrUnimplemented("Sync")
}

func (Unimplemented) GetSettingsSchema(context.Context, *metadatav1.GetSettingsSchemaRequest) (*metadatav1.GetSettingsSchemaResponse, error) {
	return &metadatav1.GetSettingsSchemaResponse{}, nil
}

func (Unimplemented) GetSettings(context.Context, *metadatav1.GetSettingsRequest) (*metadatav1.GetSettingsResponse, error) {
	return &metadatav1.GetSettingsResponse{}, nil
}

func (Unimplemented) SetSettings(context.Context, *metadatav1.SetSettingsRequest) (*metadatav1.SetSettingsResponse, error) {
	return &metadatav1.SetSettingsResponse{}, nil
}

func (Unimplemented) Health(context.Context, *metadatav1.HealthRequest) (*metadatav1.HealthResponse, error) {
	return &metadatav1.HealthResponse{Status: metadatav1.HealthStatus_HEALTH_STATUS_OK}, nil
}
