# Progress

The plan for the first version of meta and its first plugin, [meta-mangabaka](https://github.com/panelarr/meta-mangabaka). Each slice works end to end, has tests, and ends with `make check` green in every repo it touches.

| # | Slice | Repo | Branch | State |
|---|---|---|---|---|
| 1 | Scaffold both repos (Makefile, lint, CI with Postgres, community files, docs) | meta, meta-mangabaka | `chore/repo-scaffold` | done |
| 2 | Plugin protocol + meta API protos, Go plugin SDK (managed + standalone), conformance harness | meta | `feat/proto-metadata-v1` | done |
| 3 | Database: goose migrations for the entry schema, sqlc queries, pgx pool, tests on real Postgres | meta | | |
| 4 | MangaBaka `dump` ingestion: download, checksum, zstd, SQLite read, mapping, merges, checkpoints | meta-mangabaka | | |
| 5 | Ingest: plugin host, scheduler, batched upserts, merged entries, merges, removals, sync runs | meta | | |
| 6 | API: browse, search (Postgres FTS + trigram), get, external lookup, admin, OpenAPI | meta | | |
| 7 | MangaBaka `lookup`: search/get through the API within its rate limits | meta-mangabaka | | |
| 8 | Images, compose example, quick starts, docs | meta, meta-mangabaka | | |

## Next

Slice 3: database (goose migrations, sqlc queries, pgx).

## Notes

- Until meta publishes tagged versions, meta-mangabaka builds against a local meta clone (see its CONTRIBUTING).
