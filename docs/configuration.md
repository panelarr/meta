# Configuration

meta reads an optional YAML file (`--config FILE` or `META_CONFIG`) and then a few environment variables. Inside the file, `${NAME}` and `${NAME:-default}` are replaced with environment values, which keeps secrets out of the file. [`meta.example.yaml`](../meta.example.yaml) is a commented starting point.

## Commands

```sh
meta serve   [--config FILE] [--database-url URL]          # run the service
meta sync    PROVIDER [KIND] [--force] [--config FILE]     # one sync in the foreground, then exit
meta rebuild [--config FILE]                              # rebuild every entry from the stored records
meta migrate [up|down|status] [--config FILE] [--database-url URL]
meta admin-token reset [--config FILE]                    # new admin token, printed once
meta healthcheck [--url URL]                              # probe a running meta
meta version
```

`serve` and `sync` apply pending migrations before they start.

## Settings

| Key | Env | Default | What |
|---|---|---|---|
| `listen` | `META_LISTEN` | `:8901` | API address. gRPC, gRPC-Web, Connect and REST/JSON share it |
| `admin_token` | `META_ADMIN_TOKEN` | generated | The admin's bearer token (see [Admin token](#admin-token)) |
| `data_dir` | `META_DATA_DIR` | your config directory + `/panelarr-meta` (`/data` in the image) | Files meta keeps outside the database: the generated encryption key |
| `secret_key` | `META_SECRET_KEY` | generated | Base64 of 32 bytes (`openssl rand -base64 32`). Encrypts stored secrets such as proxy passwords. Without it, meta generates `secret.key` in `data_dir` the first time it stores a secret. **Keep it with your database backups** |
| `api_keys` | `META_API_KEYS` (comma-separated) | none | **Deprecated.** Keys with full access, kept so older setups keep working. Create [clients](api.md#clients-and-scopes-metaclientsservice) with scoped keys instead |
| `database_url` | `META_DATABASE_URL` | required | PostgreSQL URL |
| `search.backend` | `META_SEARCH_BACKEND` | `postgres` | `postgres`, `typesense` or `meilisearch` (see [search](search.md)). Settings saved in the admin win over these |
| `search.typesense.url`, `.api_key`, `.index_prefix` | `META_TYPESENSE_URL`, `META_TYPESENSE_API_KEY` | prefix `meta` | Typesense server and a key that may manage collections and aliases |
| `search.meilisearch.url`, `.api_key`, `.index_prefix` | `META_MEILISEARCH_URL`, `META_MEILISEARCH_API_KEY` | prefix `meta` | Meilisearch server and a key that may manage indexes |
| `search.browse` | | `facets` | Who serves browsing with an engine: `facets` (the engine when facets are asked for), `engine`, `postgres` |
| `search.timeout` | | `2s` | How long a query waits for the engine before PostgreSQL answers |
| `search.outbox.batch`, `.interval`, `.rebuild_threshold` | | `500`, `1s`, `50000` | Changes applied per round, how often the indexer looks, and the backlog above which the index is rebuilt instead |
| `merge.priority` | | config order | Preferred provider per field (see [Merging](#merging)) |
| `merge.display_language` | | `en` | Language of display titles when a request doesn't ask for one |
| `ingest.batch_size` | | `500` | Records per transaction when a plugin sends checkpoints rarely |
| `ingest.stale_after` | | `168h` | Age after which a lookup refreshes an entry from its providers |
| `jobs.retention` | | `100` | Finished runs kept per job, provider syncs included |
| `jobs.stale_refresh.schedule` | | none (manual only) | Cron expression (UTC) for the stale-entry refresh job |
| `jobs.stale_refresh.limit` | | `100` | Most entries one stale-entry refresh looks up |
| `duplicates.*` | `META_JUDGE_API_KEY` | see [duplicates](duplicates.md#settings) | The duplicates job and the optional decision model |
| `metrics.enabled` | `META_METRICS_ENABLED` | `false` | Prometheus metrics (see [observability](observability.md)) |
| `metrics.listen` | `META_METRICS_LISTEN` | `:9464` | Address of the metrics endpoint. Empty: `/metrics` on the API port for the `metrics:read` scope |
| `log.level` | `META_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `log.format` | `META_LOG_FORMAT` | `json` | `json` or `text` |
| `providers` | | none | Metadata plugins (below) |

## Admin token

The admin token has full access and is how you sign in to the admin. Set your own with `META_ADMIN_TOKEN`. Otherwise meta generates one on first start, prints it **once** to standard output (not the log) and stores only its hash in the database:

```
========================================================================
 Generated admin token (shown only now, stored only as a hash):
   mta_…
 Set META_ADMIN_TOKEN to use your own token instead.
========================================================================
```

Lost it? Run `meta admin-token reset` (it prints a new one) and restart meta, or set `META_ADMIN_TOKEN`. Callers other than the admin should use [clients](api.md#clients-and-scopes-metaclientsservice) with their own keys and only the scopes they need.

## Providers

meta ships with **no** providers. Each entry adds one metadata plugin:

```yaml
providers:
  - id: mangabaka              # the provider key the plugin reports
    remote:                    # a plugin running as a service...
      url: http://meta-mangabaka:8903
      api_key: ${MANGABAKA_PLUGIN_KEY}
      connect: false           # true: use the Connect protocol instead of gRPC
    # managed:                 # ...or a binary meta starts itself
    #   path: /plugins/meta-mangabaka
    #   args: []
    #   env: []
    settings: {...}            # sent to the plugin (its settings schema says what it accepts)
    schedules:                 # cron (5 fields, UTC) or "@every 6h", per ingestion kind
      dump: "0 3 * * *"
    disabled: false
```

- meta checks that the plugin reports the configured `id`, sends `settings` with `SetSettings`, and checks health every minute. Unreachable plugins are retried with back-off.
- The limits a plugin declares for each ingestion type (requests per period, concurrency) are enforced by meta, on top of the plugin's own limits.
- Without schedules, a provider only syncs when an admin asks (`RunSync` or `meta sync`).

## Merging

Each entry is built from the records of every provider that knows the work. For each field (`titles`, `credits`, `tags`, `images`, ...: the field names of the [entry schema](schema.md)) the value comes from the most preferred provider that has one:

```yaml
merge:
  priority:
    "*": [mangabaka, anilist]   # default order
    credits: [anilist]          # people from AniList first
```

Titles are the exception: every provider's titles are kept (without duplicates), because more titles make search better. A curator can lock a field to a value or to a provider; locks win over priority.

## Database

meta stores everything in **PostgreSQL** (version 14 or newer; CI runs 18):

```sh
export META_DATABASE_URL='postgres://meta:secret@db:5432/meta?sslmode=disable'
meta migrate        # apply migrations (meta also applies them at startup)
meta migrate status # list migrations
meta migrate down   # roll back the latest one
```

meta keeps everything **inside its own database**, so it can share a PostgreSQL server with other services:

- The role needs to own the database, or have `CREATE` on it. It needs **no superuser** rights and changes no server-wide settings.
- The only extension is `pg_trgm` (for search). It is a *trusted* extension, so the database owner can create it. If your server disallows it, a superuser can run `CREATE EXTENSION pg_trgm` in meta's database once.
- To keep meta's tables in their own schema, create the schema and add it to the URL: `...?search_path=meta`.
- Migrations take a PostgreSQL advisory lock, so several meta instances can start at once safely.

Setting up a database on a shared server, as a superuser:

```sql
CREATE ROLE meta LOGIN PASSWORD 'secret';
CREATE DATABASE meta OWNER meta;
```

## Health

- `GET /healthz`: the process is up (no key needed).
- `GET /readyz`: the database answers (no key needed).
- `GET /openapi.yaml`: the OpenAPI document of the REST API (no key needed).
