# Writing a metadata plugin

This guide builds a plugin with the Go SDK. A plugin in any other language only needs to implement the `panelarr.metadata.v1` protos as a standard **gRPC** service and run in [standalone mode](protocol.md#run-modes); meta talks to remote plugins over gRPC.

The SDK and the protos are Apache-2.0: your plugin can use any licence.

## 1. Implement `plugin.Plugin`

Embed `plugin.Unimplemented` and override what your provider supports. A dump-only plugin needs `GetInfo`, `Sync` and usually the settings calls:

```go
package main

import (
	"context"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/sdk/plugin"
)

type myPlugin struct {
	plugin.Unimplemented
}

func (myPlugin) GetInfo(context.Context, *metadatav1.GetInfoRequest) (*metadatav1.GetInfoResponse, error) {
	return &metadatav1.GetInfoResponse{Info: &metadatav1.PluginInfo{
		Id:        "example",
		Name:      "Example provider",
		Version:   "0.1.0",
		Provider:  "example",
		Protocols: []string{plugin.Protocol},
		Ingestions: []*metadatav1.Ingestion{
			{Kind: plugin.KindDump, FullSnapshot: true, ScheduleHint: "0 4 * * *"},
		},
		Capabilities: []string{"titles", "tags"},
		Attribution:  &metadatav1.Attribution{ProviderName: "Example", License: "CC BY 4.0"},
	}}, nil
}

func (myPlugin) Sync(ctx context.Context, req *metadatav1.SyncRequest, send plugin.SendFunc) error {
	w := plugin.NewSyncWriter(send)
	w.CheckpointEvery = 1000
	if err := w.Started(&metadatav1.SyncStarted{Kind: req.GetKind(), FullSnapshot: true}); err != nil {
		return err
	}
	for _, rec := range readEverything(ctx) {
		if err := w.Upsert(toProviderEntry(rec)); err != nil {
			return err // the host went away
		}
	}
	return w.Finished("")
}

func main() {
	plugin.Main(myPlugin{}, plugin.WithDefaultListen(":8903"))
}
```

`plugin.Main` handles both run modes and the `--listen` / `--api-key` flags (or `PLUGIN_LISTEN` / `PLUGIN_API_KEY`).

## 2. Map records to the entry schema

Fill what your provider knows; leave the rest empty. See [schema.md](schema.md). Some rules that make merging work:

- `ref` is your provider key plus your ID for the record, always as a string.
- Put every title in `titles` with its language and kind. Mark the main one `is_primary`.
- For every enum, set the closest value **and** the provider's original string in the `raw_*` field. Unknown values map to `OTHER` or `UNKNOWN`; the raw value keeps the meaning.
- Put IDs of the same work at other sites in `external_ids` (`anilist`, `mal`, `mangaupdates`, ...). meta links records that share an ID.
- People known only by name become a `Person` with just a name.
- Keep the original record in `raw` so nothing is lost.
- Set `attribution` (or a default in `GetInfo`) so meta can credit the provider.

## 3. Settings

Describe settings with a JSON Schema and validate with `sdk/settings`:

```go
var schema, _ = settings.Parse(`{"type":"object","properties":{"dir":{"type":"string","default":"/data"}}}`)

func (p *myPlugin) SetSettings(_ context.Context, r *metadatav1.SetSettingsRequest) (*metadatav1.SetSettingsResponse, error) {
	values := r.GetSettings().AsMap()
	if err := schema.Validate(values); err != nil {
		return nil, plugin.ErrInvalidArgument("%v", err)
	}
	values = schema.WithDefaults(values)
	// apply values ...
	st, _ := structpb.NewStruct(values)
	return &metadatav1.SetSettingsResponse{Settings: st}, nil
}
```

`settings.Parse` stamps the order the properties are written in onto each of them as `"x-order": 1, 2, …`, at every depth (nested objects and array items included), because the schema's key order is lost on its way to a host's form. Set `x-order` (or `propertyOrder`) yourself to override it for a property.

## 4. Network profiles

Declare the `network_profile` common service and make every request with an `httpx.Client`: it follows the profile the host sends (proxy, SOCKS5 with credentials, challenge solver, rate limit, concurrency, user agent, headers, timeouts, DNS over HTTPS) and keeps one cookie jar.

```go
type myPlugin struct {
	plugin.Unimplemented
	net *httpx.Client
}

func newPlugin() *myPlugin {
	return &myPlugin{net: httpx.New(httpx.Options{UserAgent: "my-plugin/1.0 (+https://example.org)"})}
}

// In GetInfo:
//   CommonServices: []*metadatav1.CommonService{{Name: "network_profile", Purpose: "downloads and API calls"}},

func (p *myPlugin) SetNetworkProfile(ctx context.Context, r *metadatav1.SetNetworkProfileRequest) (*metadatav1.SetNetworkProfileResponse, error) {
	return p.net.SetNetworkProfile(ctx, r)
}

// Everywhere else: p.net.HTTP().Do(req)
```

Your own user agent is used unless the profile sets one. When a site answers with an anti-bot challenge and the profile has no solver, requests fail with an `httpx.ChallengeError` that tells the admin what to set up; return it as `unavailable`.

## 5. Respect the provider

- Declare your limits in `GetInfo` and enforce them in the plugin as well.
- Send a clear `User-Agent` that names your plugin.
- Return `plugin.ErrRateLimited(retryAfter, ...)` when the provider says to slow down.
- Never ship the provider's data with your plugin. Download it when the person running it asks.

## 6. Test it

```go
func TestConformance(t *testing.T) {
	conformance.Run(t, client.InProcess(&myPlugin{}), conformance.Options{})
}
```

`sdk/plugintest.Fake` is a configurable in-memory plugin for testing hosts. [`sdk/examples/static-plugin`](../sdk/examples/static-plugin) is a complete, tiny plugin.

## 7. Run it

```sh
go build -o my-plugin .
./my-plugin --listen :8903 --api-key "$KEY"   # standalone
```

Point meta at it as a remote provider, or let meta start the binary as a managed provider (see [configuration.md](configuration.md)).
