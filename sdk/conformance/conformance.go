// SPDX-License-Identifier: Apache-2.0

// Package conformance checks that a metadata plugin follows the
// panelarr.metadata.v1 protocol: a valid GetInfo, a usable settings schema,
// and a well-formed Sync stream. Run it from a plugin's own tests with
// [Run], or against any client with [Check].
package conformance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/sdk/client"
	"github.com/panelarr/meta/sdk/plugin"
	"github.com/panelarr/meta/sdk/settings"
)

// Options select what to exercise.
type Options struct {
	// SyncKind is the ingestion kind to sync. Empty picks the first of
	// "dump" and "delta" the plugin declares; if it declares neither, no
	// sync is run.
	SyncKind string
	// SyncRequest overrides the request sent to Sync.
	SyncRequest *metadatav1.SyncRequest
	// SkipSync skips the Sync check.
	SkipSync bool
	// Settings are applied with SetSettings before anything else, e.g. to
	// point the plugin at a test server.
	Settings map[string]any
	// GetRef, when set, is fetched with Get. When empty and the plugin
	// declares "lookup" or "scrape", the first synced record is fetched.
	GetRef *metadatav1.ExternalId
	// SearchQuery, when set and the plugin declares "lookup" or "scrape",
	// is sent to Search.
	SearchQuery string
	// Timeout bounds the whole check. Default 5 minutes.
	Timeout time.Duration
}

// Report is what Check found.
type Report struct {
	Info *metadatav1.PluginInfo
	// Problems break the protocol. A conformant plugin has none.
	Problems []string
	// Warnings are likely mistakes that don't break the protocol.
	Warnings []string
	// Counts of sync events by kind ("started", "upsert", ...).
	Events map[string]int64
}

func (r *Report) problem(format string, args ...any) {
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// OK reports whether no problems were found.
func (r *Report) OK() bool { return len(r.Problems) == 0 }

// Run checks the plugin behind c and fails t for every problem. Warnings
// are logged.
func Run(t testing.TB, c client.Client, opts Options) *Report {
	t.Helper()
	rep, err := Check(context.Background(), c, opts)
	if err != nil {
		t.Fatalf("conformance: %v", err)
	}
	for _, w := range rep.Warnings {
		t.Logf("conformance warning: %s", w)
	}
	for _, p := range rep.Problems {
		t.Errorf("conformance: %s", p)
	}
	return rep
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Check runs the checks. The error is for failures to run them at all
// (e.g. the plugin can't be reached); protocol violations go to the report.
func Check(ctx context.Context, c client.Client, opts Options) (*Report, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	rep := &Report{Events: map[string]int64{}}

	info, err := c.GetInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("get info: %w", err)
	}
	rep.Info = info
	checkInfo(rep, info)

	checkSettings(ctx, rep, c, opts.Settings)

	checkNetworkProfile(ctx, rep, c, info)

	if h, err := c.Health(ctx); err != nil {
		rep.problem("Health: %v", err)
	} else if h.GetStatus() == metadatav1.HealthStatus_HEALTH_STATUS_UNSPECIFIED {
		rep.problem("Health: status is unspecified")
	}

	var first *metadatav1.ExternalId
	if !opts.SkipSync {
		kind := opts.SyncKind
		if kind == "" {
			for _, k := range []string{plugin.KindDump, plugin.KindDelta} {
				if declares(info, k) {
					kind = k
					break
				}
			}
		}
		if kind != "" {
			req := opts.SyncRequest
			if req == nil {
				req = &metadatav1.SyncRequest{Kind: kind}
			}
			first, err = checkSync(ctx, rep, c, info, req)
			if err != nil {
				return nil, err
			}
		}
	}

	if declares(info, plugin.KindLookup) || declares(info, plugin.KindScrape) {
		ref := opts.GetRef
		if ref == nil {
			ref = first
		}
		if ref != nil {
			checkGet(ctx, rep, c, info, ref)
		}
		if opts.SearchQuery != "" {
			checkSearch(ctx, rep, c, info, opts.SearchQuery)
		}
	}
	return rep, nil
}

func declares(info *metadatav1.PluginInfo, kind string) bool {
	for _, in := range info.GetIngestions() {
		if in.GetKind() == kind {
			return true
		}
	}
	return false
}

func checkInfo(rep *Report, info *metadatav1.PluginInfo) {
	if !idPattern.MatchString(info.GetId()) {
		rep.problem("GetInfo: id %q must be lower case letters, digits, '-' or '_'", info.GetId())
	}
	if info.GetName() == "" {
		rep.problem("GetInfo: name is empty")
	}
	if info.GetVersion() == "" {
		rep.problem("GetInfo: version is empty")
	}
	if !idPattern.MatchString(info.GetProvider()) {
		rep.problem("GetInfo: provider key %q must be lower case letters, digits, '-' or '_'", info.GetProvider())
	}
	if !slices.Contains(info.GetProtocols(), plugin.Protocol) {
		rep.problem("GetInfo: protocols must include %q", plugin.Protocol)
	}
	if len(info.GetIngestions()) == 0 {
		rep.problem("GetInfo: declares no ingestion types")
	}
	seen := map[string]bool{}
	for _, in := range info.GetIngestions() {
		if in.GetKind() == "" {
			rep.problem("GetInfo: an ingestion has no kind")
			continue
		}
		if seen[in.GetKind()] {
			rep.problem("GetInfo: ingestion %q declared twice", in.GetKind())
		}
		seen[in.GetKind()] = true
		for _, rl := range in.GetLimits().GetRateLimits() {
			if rl.GetRequests() <= 0 || rl.GetPer().AsDuration() <= 0 {
				rep.problem("GetInfo: ingestion %q has a rate limit without requests or period", in.GetKind())
			}
			if rl.GetOperation() == "" {
				rep.problem("GetInfo: ingestion %q has a rate limit without an operation", in.GetKind())
			}
		}
	}
	for _, cs := range info.GetCommonServices() {
		if cs.GetName() == "" {
			rep.problem("GetInfo: a common service has no name")
		}
	}
	if info.GetAttribution().GetProviderName() == "" {
		rep.warn("GetInfo: no default attribution; every record must then carry its own")
	}
}

func checkSettings(ctx context.Context, rep *Report, c client.Client, values map[string]any) {
	res, err := c.GetSettingsSchema(ctx)
	if err != nil {
		rep.problem("GetSettingsSchema: %v", err)
		return
	}
	var schema *settings.Schema
	if len(res.GetSchema().GetFields()) > 0 {
		schema, err = settings.FromStruct(res.GetSchema())
		if err != nil {
			rep.problem("GetSettingsSchema: %v", err)
		}
	}
	if values != nil {
		st, err := toStruct(values)
		if err != nil {
			rep.problem("settings: %v", err)
			return
		}
		set, err := c.SetSettings(ctx, &metadatav1.SetSettingsRequest{Settings: st})
		if err != nil {
			rep.problem("SetSettings: %v", err)
			return
		}
		if schema != nil {
			if err := schema.Validate(set.GetSettings().AsMap()); err != nil {
				rep.problem("SetSettings: returned settings don't match the schema: %v", err)
			}
		}
	}
	got, err := c.GetSettings(ctx)
	if err != nil {
		rep.problem("GetSettings: %v", err)
		return
	}
	if schema != nil {
		if err := schema.Validate(got.GetSettings().AsMap()); err != nil {
			rep.problem("GetSettings: settings don't match the schema: %v", err)
		}
	}
}

func refString(r *metadatav1.ExternalId) string {
	return r.GetProvider() + ":" + r.GetId()
}

func checkSync(ctx context.Context, rep *Report, c client.Client, info *metadatav1.PluginInfo, req *metadatav1.SyncRequest) (*metadatav1.ExternalId, error) {
	stream, err := c.Sync(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("sync: %w", err)
	}
	defer func() { _ = stream.Close() }()

	var (
		first        *metadatav1.ExternalId
		started      *metadatav1.SyncStarted
		finished     *metadatav1.SyncFinished
		lastCount    int64
		upserts      int64
		removals     int64
		merges       int64
		index        int
		seen         = map[string]bool{}
		defaultAttrb = info.GetAttribution().GetProviderName() != ""
	)
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			rep.problem("Sync: stream failed after %d events: %v", index, err)
			return first, nil
		}
		index++
		if finished != nil {
			rep.problem("Sync: event %d arrives after SyncFinished", index)
		}
		switch e := ev.GetEvent().(type) {
		case *metadatav1.SyncResponse_Started:
			rep.Events["started"]++
			if index != 1 {
				rep.problem("Sync: SyncStarted is event %d, must be the first", index)
			}
			started = e.Started
			if started.GetKind() != req.GetKind() {
				rep.problem("Sync: SyncStarted kind %q, requested %q", started.GetKind(), req.GetKind())
			}
		case *metadatav1.SyncResponse_Upsert:
			rep.Events["upsert"]++
			upserts++
			entry := e.Upsert
			ref := entry.GetRef()
			key := refString(ref)
			if first == nil {
				first = ref
			}
			if ref.GetProvider() != info.GetProvider() {
				rep.problem("Sync: upsert %s: provider must be %q", key, info.GetProvider())
			}
			if ref.GetId() == "" {
				rep.problem("Sync: upsert at event %d has no id", index)
			}
			if seen[key] {
				rep.problem("Sync: upsert %s sent twice in one run", key)
			}
			seen[key] = true
			checkEntry(rep, key, entry, defaultAttrb)
		case *metadatav1.SyncResponse_Removed:
			rep.Events["removed"]++
			removals++
			if e.Removed.GetRef().GetId() == "" {
				rep.problem("Sync: removal at event %d has no id", index)
			}
		case *metadatav1.SyncResponse_Merge:
			rep.Events["merge"]++
			merges++
			m := e.Merge
			if m.GetMerged().GetId() == "" || m.GetInto().GetId() == "" {
				rep.problem("Sync: merge at event %d is missing an id", index)
			}
			if refString(m.GetMerged()) == refString(m.GetInto()) {
				rep.problem("Sync: merge %s into itself", refString(m.GetMerged()))
			}
			if m.GetInto().GetProvider() != info.GetProvider() || m.GetMerged().GetProvider() != info.GetProvider() {
				rep.problem("Sync: merge %s -> %s: provider must be %q", refString(m.GetMerged()), refString(m.GetInto()), info.GetProvider())
			}
		case *metadatav1.SyncResponse_Checkpoint:
			rep.Events["checkpoint"]++
			cp := e.Checkpoint
			if cp.GetCursor() == "" {
				rep.problem("Sync: checkpoint at event %d has no cursor", index)
			}
			if cp.GetCount() < lastCount {
				rep.problem("Sync: checkpoint count went down (%d after %d)", cp.GetCount(), lastCount)
			}
			if cp.GetCount() != upserts+removals+merges {
				rep.problem("Sync: checkpoint count %d, but %d records were sent", cp.GetCount(), upserts+removals+merges)
			}
			lastCount = cp.GetCount()
		case *metadatav1.SyncResponse_Finished:
			rep.Events["finished"]++
			finished = e.Finished
		case *metadatav1.SyncResponse_Notice:
			rep.Events["notice"]++
			if e.Notice.GetMessage() == "" {
				rep.problem("Sync: notice at event %d has no message", index)
			}
		case *metadatav1.SyncResponse_Progress:
			rep.Events["progress"]++
		default:
			rep.problem("Sync: event %d is empty", index)
		}
	}
	if started == nil {
		rep.problem("Sync: no SyncStarted event")
	}
	if finished == nil {
		rep.problem("Sync: no SyncFinished event")
		return first, nil
	}
	if finished.GetUpserts() != upserts || finished.GetRemovals() != removals || finished.GetMerges() != merges {
		rep.problem("Sync: SyncFinished counts (upserts %d, removals %d, merges %d) don't match the stream (%d, %d, %d)",
			finished.GetUpserts(), finished.GetRemovals(), finished.GetMerges(), upserts, removals, merges)
	}
	if started != nil && started.GetUnchanged() && upserts+removals+merges > 0 {
		rep.problem("Sync: SyncStarted says unchanged, but records were sent")
	}
	if started != nil && started.GetExpectedCount() > 0 && started.GetExpectedCount() != upserts+finished.GetSkipped() {
		rep.warn("Sync: expected %d records, got %d upserts and %d skipped", started.GetExpectedCount(), upserts, finished.GetSkipped())
	}
	if rep.Events["checkpoint"] == 0 && upserts > 0 {
		rep.warn("Sync: no checkpoints; the host can only commit at the end")
	}
	return first, nil
}

func checkEntry(rep *Report, key string, e *metadatav1.ProviderEntry, defaultAttribution bool) {
	if len(e.GetTitles()) == 0 {
		rep.problem("record %s has no titles", key)
	}
	for i, t := range e.GetTitles() {
		if t.GetText() == "" {
			rep.problem("record %s: title %d is empty", key, i)
		}
	}
	if e.GetAttribution().GetProviderName() == "" && !defaultAttribution {
		rep.problem("record %s has no attribution and the plugin declares no default", key)
	}
	if e.GetState() == metadatav1.EntryState_ENTRY_STATE_MERGED && e.GetMergedInto().GetId() == "" {
		rep.problem("record %s is merged but has no merged_into", key)
	}
	if e.GetType() == metadatav1.EntryType_ENTRY_TYPE_OTHER && e.GetRawType() == "" {
		rep.warn("record %s: type is other but raw_type is empty", key)
	}
	for _, x := range e.GetExternalIds() {
		if x.GetProvider() == "" || x.GetId() == "" {
			rep.problem("record %s has an external id without provider or id", key)
		}
	}
	for _, ta := range e.GetTags() {
		if ta.GetTag().GetName() == "" {
			rep.problem("record %s has a tag without a name", key)
		}
	}
	for _, cr := range e.GetCredits() {
		if cr.GetPerson().GetName() == "" {
			rep.problem("record %s has a credit without a person name", key)
		}
	}
	for _, img := range e.GetImages() {
		if img.GetUrl() == "" && len(img.GetVariants()) == 0 {
			rep.problem("record %s has an image without a url", key)
		}
	}
}

func checkGet(ctx context.Context, rep *Report, c client.Client, info *metadatav1.PluginInfo, ref *metadatav1.ExternalId) {
	e, err := c.Get(ctx, ref)
	if err != nil {
		rep.problem("Get %s: %v", refString(ref), err)
		return
	}
	if ref.GetProvider() == info.GetProvider() && refString(e.GetRef()) != refString(ref) {
		rep.problem("Get %s returned %s", refString(ref), refString(e.GetRef()))
	}
	checkEntry(rep, refString(e.GetRef()), e, info.GetAttribution().GetProviderName() != "")
	if _, err := c.Get(ctx, &metadatav1.ExternalId{Provider: info.GetProvider(), Id: "conformance-does-not-exist-0"}); plugin.ErrorCode(err) != plugin.CodeNotFound {
		rep.problem("Get of a missing record must fail with not_found, got %v", err)
	}
}

func checkSearch(ctx context.Context, rep *Report, c client.Client, info *metadatav1.PluginInfo, q string) {
	res, err := c.Search(ctx, &metadatav1.SearchRequest{Query: q, Limit: 5})
	if err != nil {
		rep.problem("Search %q: %v", q, err)
		return
	}
	if len(res.GetEntries()) > 5 {
		rep.problem("Search: asked for 5 results, got %d", len(res.GetEntries()))
	}
	for _, e := range res.GetEntries() {
		checkEntry(rep, refString(e.GetRef()), e, info.GetAttribution().GetProviderName() != "")
	}
}

func toStruct(m map[string]any) (*structpb.Struct, error) {
	return structpb.NewStruct(m)
}

func checkNetworkProfile(ctx context.Context, rep *Report, c client.Client, info *metadatav1.PluginInfo) {
	declared := false
	for _, cs := range info.GetCommonServices() {
		declared = declared || cs.GetName() == "network_profile"
	}
	if !declared {
		return
	}
	sample := &metadatav1.NetworkProfile{
		Name:      "conformance",
		Headers:   map[string]string{"X-Conformance": "1"},
		UserAgent: &metadatav1.UserAgent{Mode: &metadatav1.UserAgent_RandomDesktop{RandomDesktop: true}},
		RateLimit: &metadatav1.NetworkRateLimit{Requests: 1000, Period: durationpb.New(time.Second)},
	}
	if err := c.SetNetworkProfile(ctx, sample); err != nil {
		rep.problem("SetNetworkProfile: declares network_profile but rejects a profile: %v", err)
	}
	if err := c.SetNetworkProfile(ctx, nil); err != nil {
		rep.problem("SetNetworkProfile: can't go back to a direct connection: %v", err)
	}
}
