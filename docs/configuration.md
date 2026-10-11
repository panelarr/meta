# Configuration

meta reads an optional YAML file (`--config FILE` or `META_CONFIG`) and then a few environment variables. Inside the file, `${NAME}` and `${NAME:-default}` are replaced with environment values, which keeps secrets out of the file. [`meta.example.yaml`](../meta.example.yaml) is a commented starting point.

## Commands

```sh
meta serve   [--config FILE] [--database-url URL]          # run the service
meta sync    PROVIDER [KIND] [--force] [--config FILE]     # one sync in the foreground, then exit
meta migrate [up|down|status] [--config FILE] [--database-url URL]
meta version
```

`serve` and `sync` apply pending migrations before they start.

## Settings

| Key | Env | Default | What |
|---|---|---|---|
| `listen` | `META_LISTEN` | `:8901` | API address. gRPC, gRPC-Web, Connect and REST/JSON share it |
| `api_keys` | `META_API_KEYS` (comma-separated) | none | Keys callers send as `Authorization: Bearer <key>`. With none, the API is open: only do that on a private network |
| `database_url` | `META_DATABASE_URL` | required | PostgreSQL URL |
| `search.backend` | `META_SEARCH_BACKEND` | `postgres` | Search backend. Only `postgres` exists today; others plug in behind the same interface later |
| `merge.priority` | | config order | Preferred provider per field (see [Merging](#merging)) |
| `merge.display_language` | | `en` | Language of display titles when a request doesn't ask for one |
| `ingest.batch_size` | | `500` | Records per transaction when a plugin sends checkpoints rarely |
| `ingest.stale_after` | | `168h` | Age after which a lookup refreshes an entry from its providers |
| `log.level` | `META_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `log.format` | `META_LOG_FORMAT` | `json` | `json` or `text` |
| `providers` | | none | Metadata plugins (below) |

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
