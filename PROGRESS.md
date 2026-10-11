# Progress

The plan for the first version of meta and its first plugin, [meta-mangabaka](https://github.com/panelarr/meta-mangabaka). Each slice works end to end, has tests, and ends with `make check` green in every repo it touches.

| # | Slice | Repo | Branch | State |
|---|---|---|---|---|
| 1 | Scaffold both repos (Makefile, lint, CI with Postgres, community files, docs) | meta, meta-mangabaka | `chore/repo-scaffold` | done |
| 2 | Plugin protocol + meta API protos, Go plugin SDK (managed + standalone), conformance harness | meta | `feat/proto-metadata-v1` | done |
| 3 | Database: goose migrations for the entry schema, sqlc queries, pgx pool, tests on real Postgres | meta | `feat/db-entry-schema` | done |
| 4 | MangaBaka `dump` ingestion: download, checksum, zstd, SQLite read, mapping, merges, checkpoints | meta-mangabaka | `feat/dump-ingest` | done |
| 5 | Ingest: plugin host, scheduler, batched upserts, merged entries, merges, removals, sync runs | meta | `feat/ingest` | done |
| 6 | API: browse, search (Postgres FTS + trigram), get, external lookup, admin, OpenAPI | meta | `feat/api-catalog` | in progress: code written, tests not yet |
| 7 | MangaBaka `lookup`: search/get through the API within its rate limits | meta-mangabaka | `feat/api-lookup` | done |
| 8 | Images, compose example, quick starts, docs | meta, meta-mangabaka | | |

## Next

Slice 6: write tests for internal/search and internal/api/meta.go (browse filters/sorts/paging, search by title/CJK/typo/person, GetEntry redirect, LookupExternal via plugin, ListTags, REST paths), then `make check` and merge. Slice 8 (meta-mangabaka image/release/docs) was running on its branch `feat/image-and-docs`; review it. Then slice 8 for meta (Dockerfile, compose, docs/api.md) and DONE.

## Notes

- Until meta publishes tagged versions, meta-mangabaka builds against a local meta clone (see its CONTRIBUTING).
