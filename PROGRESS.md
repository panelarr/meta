# Progress

The plan for the first version of meta and its first plugin, [meta-mangabaka](https://github.com/panelarr/meta-mangabaka). Each slice works end to end, has tests, and ends with `make check` green in every repo it touches.

| # | Slice | Repo | Branch | State |
|---|---|---|---|---|
| 1 | Scaffold both repos (Makefile, lint, CI with Postgres, community files, docs) | meta, meta-mangabaka | `chore/repo-scaffold` | done |
| 2 | Plugin protocol + meta API protos, Go plugin SDK (managed + standalone), conformance harness | meta | `feat/proto-metadata-v1` | done |
| 3 | Database: goose migrations for the entry schema, sqlc queries, pgx pool, tests on real Postgres | meta | `feat/db-entry-schema` | done |
| 4 | MangaBaka `dump` ingestion: download, checksum, zstd, SQLite read, mapping, merges, checkpoints | meta-mangabaka | `feat/dump-ingest` | done |
| 5 | Ingest: plugin host, scheduler, batched upserts, merged entries, merges, removals, sync runs | meta | `feat/ingest` | done |
| 6 | API: browse, search (Postgres FTS + trigram), get, external lookup, admin, OpenAPI | meta | `feat/api-catalog` | done |
| 7 | MangaBaka `lookup`: search/get through the API within its rate limits | meta-mangabaka | `feat/api-lookup` | done |
| 8 | Images, compose example, quick starts, docs | meta, meta-mangabaka | `feat/image-and-docs` (meta-mangabaka), `feat/image-and-compose` (meta) | done |

## Admin: clients, integrations and admin UI

| # | Slice | Repo | Branch | State |
|---|---|---|---|---|
| 1 | Clients and auth: admin token, scoped client keys (`catalog:read`, `admin`), `MetaClientsService` | meta | `feat/admin-clients` | done |
| 2 | Integrations: solvers, network profiles, default, per-provider assignments, secrets encrypted at rest | meta | `feat/admin-integrations` | done |
| 3 | `SetNetworkProfile` in the plugin protocol, SDK HTTP client from a profile, meta-mangabaka uses it | meta, meta-mangabaka | `feat/network-profiles` | done |
| 4 | Admin UI (React SPA) served at `/` | meta | `feat/admin-ui` | done |
| 5 | Observability: Prometheus metrics (syncs, plugin calls, search latency, API requests), off the public port by default; detailed health for the admin | meta | `feat/observability` | done |
| 6 | Image and docs | meta | `feat/image-and-docs` | done |

## Next

GOAL-0003 is done; waiting for verification. Queued: GOAL-0004 (duplicates), then GOAL-0006 (search engines).

## Notes

- Until meta publishes tagged versions, meta-mangabaka builds against a local meta clone (see its CONTRIBUTING).
