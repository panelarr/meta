# Logs, metrics and health

## Logs

meta logs one JSON object per line by default (`log.format: text` for plain lines) at the level in `log.level` (`debug`, `info`, `warn`, `error`). Every line carries `time`, `level`, `msg` and `service: "meta"`; lines from the plugin host, ingestion and the API also carry `component` (`host`, `ingest`, `api`), and sync lines carry `provider`, `kind` and `run`. Tokens, API keys, proxy credentials and cookies are never logged. The generated admin token goes to standard output once, not to the log.

## Health

| Endpoint | Auth | What |
|---|---|---|
| `GET /healthz` | none | The process is up (liveness) |
| `GET /readyz` | none | The database answers (readiness) |
| `GET /v1/admin/health` (`MetaAdminService.GetHealth`) | `admin` | Every part, with status, message, latency and details: `database` (connections), `search` (backend and its capabilities, checked with a query; with an engine also its index, document count and change backlog, and `DEGRADED` while PostgreSQL answers for it), and `provider:<id>` for each plugin (checked live: mode, plugin version, a running sync) |

The overall status of `GetHealth` is `DOWN` when the database is down, `DEGRADED` when the search backend or a plugin is down or degraded, `OK` otherwise.

## Metrics

Prometheus metrics are **off by default**. Turn them on in the configuration:

```yaml
metrics:
  enabled: true
  listen: ":9464"   # its own port (default), not the API port
```

With `listen: ""` they're served on the API port at `/metrics` instead, to callers with the `metrics:read` scope (create a client for Prometheus and set `authorization: {credentials: <key>}` in its scrape config). Environment: `META_METRICS_ENABLED=true`, `META_METRICS_LISTEN`.

| Metric | Labels | What |
|---|---|---|
| `meta_sync_runs_total` | provider, kind, status | Finished sync runs (succeeded, failed, cancelled) |
| `meta_sync_records_total` | provider, event | Sync events: upsert, changed, merge, removal, skipped, marked_removed |
| `meta_sync_duration_seconds` | provider, kind | Sync run duration (histogram) |
| `meta_sync_running` | provider | 1 while a sync runs |
| `meta_plugin_calls_total` | provider, operation, code | Plugin calls: get, search, sync, set_network_profile, health; code is the gRPC/Connect code or `ok` |
| `meta_plugin_call_duration_seconds` | provider, operation | Plugin call latency (histogram; for sync, until the stream opens) |
| `meta_plugin_up` | provider | 1 healthy, 0.5 degraded, 0 down |
| `meta_search_duration_seconds` | backend, kind | Search and browse latency (histogram) |
| `meta_job_runs_total` | job, status, trigger | Finished job runs, syncs included (succeeded, failed, cancelled, skipped) |
| `meta_job_duration_seconds` | job | Job run duration (histogram) |
| `meta_job_running` | job | 1 while a run of the job goes on in this process |
| `meta_search_fallbacks_total` | backend | Queries an engine failed and PostgreSQL answered |
| `meta_search_outbox_pending` | | Entry changes waiting for the search engine |
| `meta_search_outbox_lag_seconds` | | Age of the oldest waiting change |
| `meta_search_index_documents` | backend | Documents in the engine's index after the last rebuild |
| `meta_api_requests_total` | procedure, code | API calls by RPC and outcome (REST calls count under their RPC) |
| `meta_api_request_duration_seconds` | procedure | API latency (histogram) |

Plus the standard Go runtime (`go_*`) and process (`process_*`) metrics. Labels stay bounded: no URLs, titles, IDs of entries or credentials.
