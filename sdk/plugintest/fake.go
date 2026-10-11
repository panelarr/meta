// SPDX-License-Identifier: Apache-2.0

// Package plugintest provides a configurable fake metadata plugin for tests
// of hosts and tools.
package plugintest

import (
	"context"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/sdk/plugin"
	"github.com/panelarr/meta/sdk/settings"
)

// Merge is a provider-side merge: the record with ID Merged was merged into
// the record with ID Into.
type Merge struct {
	Merged, Into string
}

// Fake is a metadata plugin that serves records from memory. Change its
// fields between syncs (under Lock) to simulate a provider that changes.
type Fake struct {
	plugin.Unimplemented

	mu sync.Mutex
	// Provider is the provider key; default "fake".
	Provider string
	// Entries are synced in order and served by Get and Search.
	Entries []*metadatav1.ProviderEntry
	// Merges are sent after the entries.
	Merges []Merge
	// Removed IDs are sent as removals (for delta syncs).
	Removed []string
	// CheckpointEvery emits a checkpoint every N upserts; default 2.
	CheckpointEvery int64
	// SnapshotID is reported in SyncStarted. If a sync request's cursor
	// equals it and Force is not set, the sync reports "unchanged".
	SnapshotID string
	// FailAfter, when > 0, makes Sync fail after that many upserts.
	FailAfter int
	// SyncDelay is slept between events, to test cancellation.
	SyncDelay time.Duration

	// UsesNetworkProfiles makes the fake declare the network_profile common
	// service and record the profiles it receives.
	UsesNetworkProfiles bool
	profiles            []*metadatav1.NetworkProfile

	settings map[string]any
	syncs    int
}

// Profiles returns the network profiles received so far (nil entries mean
// "direct connection").
func (f *Fake) Profiles() []*metadatav1.NetworkProfile {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*metadatav1.NetworkProfile(nil), f.profiles...)
}

func (f *Fake) SetNetworkProfile(_ context.Context, req *metadatav1.SetNetworkProfileRequest) (*metadatav1.SetNetworkProfileResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.UsesNetworkProfiles {
		return nil, plugin.ErrUnimplemented("SetNetworkProfile")
	}
	var p *metadatav1.NetworkProfile
	if req.GetProfile() != nil {
		p = proto.Clone(req.GetProfile()).(*metadatav1.NetworkProfile)
	}
	f.profiles = append(f.profiles, p)
	return &metadatav1.SetNetworkProfileResponse{}, nil
}

// Lock locks the fake for changing its fields while a host may be using it.
func (f *Fake) Lock() { f.mu.Lock() }

// Unlock undoes Lock.
func (f *Fake) Unlock() { f.mu.Unlock() }

// Syncs is the number of Sync calls so far.
func (f *Fake) Syncs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncs
}

func (f *Fake) provider() string {
	if f.Provider == "" {
		return "fake"
	}
	return f.Provider
}

// Ref returns an ExternalId for id at the fake's provider.
func (f *Fake) Ref(id string) *metadatav1.ExternalId {
	return &metadatav1.ExternalId{Provider: f.provider(), Id: id}
}

const settingsSchema = `{
  "type": "object",
  "properties": {
    "greeting": {"type": "string", "default": "hello"}
  },
  "additionalProperties": false
}`

var schema = func() *settings.Schema {
	s, err := settings.Parse(settingsSchema)
	if err != nil {
		panic(err)
	}
	return s
}()

func (f *Fake) GetInfo(context.Context, *metadatav1.GetInfoRequest) (*metadatav1.GetInfoResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var services []*metadatav1.CommonService
	if f.UsesNetworkProfiles {
		services = []*metadatav1.CommonService{{Name: "network_profile", Purpose: "requests go through the host's profile"}}
	}
	return &metadatav1.GetInfoResponse{Info: &metadatav1.PluginInfo{
		CommonServices: services,
		Id:             f.provider(),
		Name:           "Fake provider",
		Version:        "0.0.0-test",
		Description:    "Serves made-up records from memory.",
		Provider:       f.provider(),
		Protocols:      []string{plugin.Protocol},
		Ingestions: []*metadatav1.Ingestion{
			{Kind: plugin.KindDump, FullSnapshot: true, ScheduleHint: "0 3 * * *"},
			{Kind: plugin.KindLookup, Limits: &metadatav1.Limits{
				RateLimits: []*metadatav1.RateLimit{{Operation: "*", Requests: 600, Per: durationpb.New(time.Minute)}},
			}},
		},
		Capabilities: []string{"titles", "tags", "people", "covers", "external_ids"},
		Attribution:  &metadatav1.Attribution{ProviderName: "Fake", License: "CC0-1.0"},
	}}, nil
}

func (f *Fake) GetSettingsSchema(context.Context, *metadatav1.GetSettingsSchemaRequest) (*metadatav1.GetSettingsSchemaResponse, error) {
	return &metadatav1.GetSettingsSchemaResponse{Schema: schema.Struct()}, nil
}

func (f *Fake) GetSettings(context.Context, *metadatav1.GetSettingsRequest) (*metadatav1.GetSettingsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, err := structpb.NewStruct(schema.WithDefaults(f.settings))
	if err != nil {
		return nil, err
	}
	return &metadatav1.GetSettingsResponse{Settings: st}, nil
}

func (f *Fake) SetSettings(_ context.Context, req *metadatav1.SetSettingsRequest) (*metadatav1.SetSettingsResponse, error) {
	values := req.GetSettings().AsMap()
	if err := schema.Validate(values); err != nil {
		return nil, plugin.ErrInvalidArgument("%v", err)
	}
	f.mu.Lock()
	f.settings = values
	f.mu.Unlock()
	st, err := structpb.NewStruct(schema.WithDefaults(values))
	if err != nil {
		return nil, err
	}
	return &metadatav1.SetSettingsResponse{Settings: st}, nil
}

func (f *Fake) Get(_ context.Context, req *metadatav1.GetRequest) (*metadatav1.GetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.Entries {
		if e.GetRef().GetId() == req.GetRef().GetId() && req.GetRef().GetProvider() == f.provider() {
			return &metadatav1.GetResponse{Entry: proto.Clone(e).(*metadatav1.ProviderEntry)}, nil
		}
		for _, x := range e.GetExternalIds() {
			if x.GetProvider() == req.GetRef().GetProvider() && x.GetId() == req.GetRef().GetId() {
				return &metadatav1.GetResponse{Entry: proto.Clone(e).(*metadatav1.ProviderEntry)}, nil
			}
		}
	}
	return nil, plugin.ErrNotFound("no record %s:%s", req.GetRef().GetProvider(), req.GetRef().GetId())
}

func (f *Fake) Search(_ context.Context, req *metadatav1.SearchRequest) (*metadatav1.SearchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := strings.ToLower(req.GetQuery())
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 10
	}
	res := &metadatav1.SearchResponse{}
	for _, e := range f.Entries {
		for _, t := range e.GetTitles() {
			if strings.Contains(strings.ToLower(t.GetText()), q) {
				res.Entries = append(res.Entries, proto.Clone(e).(*metadatav1.ProviderEntry))
				break
			}
		}
		if len(res.Entries) >= limit {
			break
		}
	}
	return res, nil
}

func (f *Fake) Sync(ctx context.Context, req *metadatav1.SyncRequest, send plugin.SendFunc) error {
	f.mu.Lock()
	f.syncs++
	entries := make([]*metadatav1.ProviderEntry, len(f.Entries))
	for i, e := range f.Entries {
		entries[i] = proto.Clone(e).(*metadatav1.ProviderEntry)
	}
	merges := append([]Merge(nil), f.Merges...)
	removed := append([]string(nil), f.Removed...)
	every := f.CheckpointEvery
	snapshot := f.SnapshotID
	failAfter := f.FailAfter
	delay := f.SyncDelay
	f.mu.Unlock()

	if every == 0 {
		every = 2
	}
	w := plugin.NewSyncWriter(send)
	w.CheckpointEvery = every
	unchanged := snapshot != "" && req.GetCursor() == snapshot && !req.GetForce()
	if err := w.Started(&metadatav1.SyncStarted{
		Kind:          req.GetKind(),
		SnapshotId:    snapshot,
		ExpectedCount: int64(len(entries)),
		FullSnapshot:  req.GetKind() == plugin.KindDump,
		Unchanged:     unchanged,
	}); err != nil {
		return err
	}
	if unchanged {
		return w.Finished(snapshot)
	}
	for i, e := range entries {
		if failAfter > 0 && i >= failAfter {
			return plugin.Errorf(plugin.CodeUnavailable, "fake failure after %d records", i)
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err := w.Upsert(e); err != nil {
			return err
		}
	}
	for _, id := range removed {
		if err := w.Removed(f.Ref(id), "gone"); err != nil {
			return err
		}
	}
	for _, m := range merges {
		if err := w.Merge(f.Ref(m.Merged), f.Ref(m.Into)); err != nil {
			return err
		}
	}
	if err := w.Checkpoint(snapshot + "#end"); err != nil {
		return err
	}
	return w.Finished(snapshot)
}

// Entry builds a simple record for tests: an ID, a primary English title,
// and optional extra titles as alternatives.
func (f *Fake) Entry(id, title string, alt ...string) *metadatav1.ProviderEntry {
	e := &metadatav1.ProviderEntry{
		Ref:    f.Ref(id),
		Type:   metadatav1.EntryType_ENTRY_TYPE_MANGA,
		Status: metadatav1.EntryStatus_ENTRY_STATUS_RELEASING,
		Titles: []*metadatav1.Title{{Text: title, Language: "en", Kind: metadatav1.TitleKind_TITLE_KIND_OFFICIAL, IsPrimary: true}},
		State:  metadatav1.EntryState_ENTRY_STATE_ACTIVE,
	}
	for _, a := range alt {
		e.Titles = append(e.Titles, &metadatav1.Title{Text: a, Kind: metadatav1.TitleKind_TITLE_KIND_ALTERNATIVE})
	}
	return e
}
