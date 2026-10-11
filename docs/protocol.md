# The metadata plugin protocol

meta gets its data from **metadata plugins**. A plugin knows one provider (a website, an API, a database dump) and turns what the provider has into records in meta's [entry schema](schema.md). meta stores the records, merges providers, and serves the catalogue.

The protocol is the protobuf package `panelarr.metadata.v1` in [`proto/panelarr/metadata/v1`](../proto/panelarr/metadata/v1). It is Apache-2.0, like the Go SDK, so plugins can use any licence.

## Run modes

The same service, `MetadataPluginService`, is spoken in both modes:

| Mode | Who starts the plugin | Transport | When to use |
|---|---|---|---|
| **Managed** | meta starts the binary as a child process | gRPC on a local socket, after the [go-plugin](https://github.com/hashicorp/go-plugin) handshake | The plugin runs next to meta |
| **Standalone** | You do (`--listen :8903`), e.g. as its own container | **gRPC** over HTTP/2 (cleartext or TLS). Go SDK plugins also answer gRPC-Web and Connect (HTTP/JSON) | The plugin runs elsewhere, or is written in another language |

**Standard gRPC is the canonical protocol.** meta calls remote plugins over gRPC, and a plugin only has to speak gRPC. The Connect protocol (which also works over HTTP/1.1 and makes `curl` easy) is an extra that plugins built with the Go SDK get for free; no host may require it.

In standalone mode, protect the plugin with an API key: callers send `Authorization: Bearer <key>`. `GET /healthz` answers without a key, for container health checks.

Plugins for meta use ports from **8903** up by convention (meta itself uses 8901).

A plugin built with the Go SDK picks its mode by itself: when meta started it, the go-plugin handshake environment is set and it serves managed; otherwise it needs `--listen`.

## What a plugin declares: `GetInfo`

```text
id, name, version, description, homepage
provider        the key used in ExternalId.provider for the plugin's own records
protocols       ["panelarr.metadata.v1"]
ingestions[]    kind, limits, schedule hint, full snapshot?
capabilities[]  "titles", "people", "tags", "covers", "relations", "lookup_by_external_id", ...
common_services[] what the host can offer that the plugin can use
attribution     default attribution for its records
```

### Ingestion types

A plugin gets data to meta in one or more ways. The list is open: hosts ignore kinds they don't know, so new kinds don't break anyone.

| Kind | RPCs | What happens | Triggered by |
|---|---|---|---|
| `lookup` | `Search`, `Get` | Ask a provider API for a few records | meta, on demand (a user action, a stale record) |
| `scrape` | `Search`, `Get` | Same, but by fetching and parsing pages | meta, on demand |
| `dump` | `Sync` | Download a full snapshot, verify it, read it, stream every record | meta's schedule or a manual run |
| `delta` | `Sync` | Stream only what changed since a cursor | meta's schedule, more often than dumps |

`full_snapshot` on an ingestion (and on `SyncStarted`) means the run lists **every** record the provider has. After such a run, meta marks records it didn't see as removed (it keeps them, so links still work).

### Limits

Each ingestion type carries its limits: rate limits per operation (`search`, `get`, `sync`, or `*`) and a concurrency cap. meta enforces them centrally, and plugins should enforce them too. Two layers keep a provider safe even when one is misconfigured.

### Common services

A plugin can list services the host offers that it can use, e.g. `network_profile` (proxy and network settings per plugin) or `solver`. The list is open. A plugin must still work without optional services.

## Settings

`GetSettingsSchema` returns a JSON Schema (draft 2020-12). meta stores the values an admin enters and sends them with `SetSettings` after it starts or connects to the plugin, and whenever they change. `SetSettings` validates them, applies defaults and returns what is in effect. The SDK's `sdk/settings` package does the validation.

## Sync streams

`Sync(SyncRequest{kind, cursor, force})` returns a stream of events:

```text
started      first event: kind, snapshot id/time, expected count, full_snapshot, unchanged
progress     work before or between records (download bytes, ...)
upsert       one ProviderEntry
removed      the provider no longer has a record
merge        the provider merged record A into record B
checkpoint   meta may commit everything before this point; carries a cursor
notice       a warning or error for the admin, recorded with the run
finished     last event: totals and the cursor for the next run
```

Rules (the conformance harness checks them):

1. `started` comes first and `finished` last. Nothing follows `finished`. Work that happens before the first record (a download, a checksum) is reported with `progress` events **after** `started`; a plugin that only learns the record count later sends `expected_count` 0 and gives the count as the `total` of a `read` progress event.
2. Every upsert's `ref.provider` is the plugin's `provider`, its ID is not empty, and it appears once per run.
3. A checkpoint's `count` is the number of record events (upserts, removals, merges) sent so far.
4. `finished` totals match what was sent. `expected_count`, when set, should equal upserts plus skipped records.
5. If the cursor in the request shows nothing changed (e.g. the same dump), the plugin may send `started{unchanged: true}` and `finished` with no records. `force` makes it send everything anyway.

meta commits at each checkpoint. If a run fails, what was committed stays.

## Errors

Errors carry a gRPC/Connect status code. The SDK's `plugin.Error` adds a retry hint (`google.rpc.RetryInfo`) for rate limits:

| Code | Meaning |
|---|---|
| `not_found` | The provider has no such record |
| `invalid_argument` | Bad request or settings |
| `unimplemented` | The plugin doesn't support the call |
| `resource_exhausted` | The provider rate-limited the plugin; retry after the hint |
| `unavailable` | The provider can't be reached right now |
| `failed_precondition` | The plugin isn't configured yet |

## Checking a plugin

`sdk/conformance` runs a plugin through `GetInfo`, the settings calls, `Health`, a full `Sync`, and `Get`/`Search` where declared, and reports every rule it breaks. Use it from your plugin's tests:

```go
conformance.Run(t, client.InProcess(myPlugin), conformance.Options{})
```

`client.Managed(path, ...)` and `client.Remote(url, ...)` give the same client for a built binary or a running service.
