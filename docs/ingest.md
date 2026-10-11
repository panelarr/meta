# How ingestion works

## Syncs

A sync asks a provider's plugin for its records (`Sync`) and writes them to meta's database. Syncs start from a schedule, from `MetaAdminService.RunSync` (`POST /v1/admin/providers/{provider}/sync`), or from `meta sync PROVIDER` in a terminal. Each provider runs at most one sync at a time.

1. meta creates a **sync run** and sends the plugin the cursor of the last finished (or last checkpointed) run.
2. The plugin streams records. meta writes them in batches and **commits at every checkpoint** (or every `ingest.batch_size` records). If a sync fails, everything committed stays, and the run records the error.
3. After a **full snapshot** (a dump), records the provider no longer has are marked **removed**. They are kept, so links and details still work. A failed or unchanged run marks nothing.
4. The cursor of the finished run is stored for the next one. If the plugin reports that nothing changed since that cursor (the same dump), the run ends at once.

Runs left "running" when meta stopped are marked failed at the next start. `GET /v1/admin/sync-runs/{id}` shows a run's counts, progress, notices and error.

## From records to entries

- **Unchanged records cost almost nothing.** Each record is hashed; a record that arrives unchanged is only marked as seen. A full re-sync of an unchanged dump changes no entry.
- **Linking.** A new record joins the entry that shares one of its IDs (its own, or the IDs it reports for other sites). Two active records of the same provider are never joined: that provider says they are different works.
- **Merged entries.** Each entry is rebuilt from its records with the per-field priority and the curator locks (see [configuration](configuration.md#merging)), computed when records change, never at read time.
- **Provider merges.** When a provider merges record A into B, A points at B's entry. If A already had an entry of its own, that entry is retired: its ID redirects to B's entry, so old links keep working. Records that arrive already merged (common in dumps) never get an entry of their own.
- **Shared people, companies and tags** are created once and reused. People and companies known only by name are shared by name.

## Performance

Writes are set-based: per batch, meta loads states, candidates and locks with a few queries, writes records and entries with batched statements, and writes entry parts with one `COPY` per table.

Measured with the synthetic scale test (made-up records shaped like a large dump: 5 titles, 2 people, 10 tags, a cover with 9 variants, 3 links, 2 relations, 4 external IDs each; 46% of them merged), PostgreSQL 18 in Docker on a laptop:

| Records | First sync | Unchanged re-sync | Database |
|---|---|---|---|
| 20,000 | 19 s | 1 s | 0.2 GB |
| 100,000 | 1 min 27 s | 6 s | 0.8 GB |

That is roughly 8 to 9 minutes for half a million records. With 54,000 active entries, browse pages take 15–60 ms (filters and sorts included) and title searches 100–300 ms.

Run it yourself:

```sh
META_SCALE=100000 META_SCALE_MERGED=46 go test -run TestScale -v -timeout 30m ./internal/ingest/
```
