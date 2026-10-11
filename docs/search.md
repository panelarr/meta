# Search and search engines

meta searches its catalogue with PostgreSQL by default: trigram similarity and substring matching over every title and the people credited (`pg_trgm`). Nothing else needs to run.

Optionally, meta answers searches from a **search engine**: [Typesense](https://typesense.org) or [Meilisearch](https://www.meilisearch.com). An engine adds faster typo-tolerant search, word-aware matching for Japanese, Chinese and Korean, and **facet counts** (how many matches have each genre, tag, type, status, content rating and original language). PostgreSQL stays the source of truth: the engine holds a copy that meta builds and keeps up to date, and PostgreSQL answers whenever the engine can't.

The library (the Panelarr server) always searches with PostgreSQL; engines are for meta's catalogue only.

## Choosing a backend

| | PostgreSQL | Typesense | Meilisearch |
|---|---|---|---|
| Runs | inside the database | a separate service; the index is held in RAM | a separate service; the index is on disk |
| Typos | trigram similarity | yes | yes |
| Inside a word | yes (any script) | Japanese, Chinese and Korean titles | no (word prefixes only) |
| Japanese, Chinese, Korean words | substring | per-field locale | per-field locale (Japanese and Chinese words segmented) |
| Facet counts | no | yes | yes |
| Licence | PostgreSQL | GPL-3.0 | MIT (meta needs no Enterprise feature) |

PostgreSQL browses and filters a large catalogue quickly, but its text search slows down as the catalogue grows: around a second per search at 300,000 entries on a laptop, where an engine answers in milliseconds (see [benchmarks](#benchmarks)). For a small catalogue, or when slower searches are fine, PostgreSQL alone is enough.

## Setting it up

Either in the configuration file (or environment):

```yaml
search:
  backend: typesense          # postgres (default), typesense or meilisearch
  typesense:
    url: http://typesense:8108
    api_key: ${TYPESENSE_API_KEY}
  meilisearch:
    url: http://meilisearch:7700
    api_key: ${MEILI_MASTER_KEY}
```

or in the admin, under **Integrations → Search engine**: pick the backend, enter its URL and key, **Test connection**, **Save**. Settings saved in the admin win over the configuration file until you choose **Use the configuration file**. The key is stored encrypted with meta's secret key (like proxy passwords) and never shown again; leave the field empty to keep it.

The compose example has profiles for both engines, with no published ports (only meta talks to them) and Meilisearch's telemetry off:

```sh
META_SEARCH_BACKEND=typesense TYPESENSE_API_KEY=$(openssl rand -hex 24) \
  docker compose --profile search-typesense up -d
```

meta talks to the engine with its key; browsers never reach the engine. All search goes through meta's API and its scopes.

## How the index stays up to date

- **Build.** When an engine is first used, or meta's document schema changed, meta builds an index in the background: the **search-rebuild** job (Jobs page). It creates a new index, fills it from every active entry, then swaps it in atomically (a Typesense collection alias, Meilisearch's `swap-indexes`) and drops the old one. Searches use the old index, or PostgreSQL before the first build, until the swap.
- **Changes.** Every change ingest makes to an entry (a new or changed record, a merge, a removal) is written to the `search_outbox` table **in the same transaction**, only while an engine is in use. An indexer in meta applies the outbox about once a second: it claims the oldest changes (several meta processes share the work), sends the entries' current state to the engine in one bulk call, and deletes the changes once the engine took them. If the engine is down, the changes wait and are applied when it is back; nothing is lost across a restart. The **search-indexer** job applies everything waiting at once.
- **Floods.** With more than `outbox.rebuild_threshold` changes waiting (a first sync, a big re-sync), meta rebuilds the index instead of applying them one by one.
- **Switching back.** With PostgreSQL as the backend, the outbox is not written, and meta forgets the engine's index: going back to the engine later builds it anew.

`meta sync` and `meta rebuild` run as separate processes write to the outbox too; the indexer of `meta serve` applies their changes.

## Fallback

When the engine fails or doesn't answer within `search.timeout` (2 s), or its index isn't built yet, PostgreSQL answers that query. The health view then reports the `search` part as **degraded** with the reason, and the event is logged at most once a minute. `/readyz` doesn't depend on the engine: meta stays ready. Each fallback is counted in `meta_search_fallbacks_total`.

## Browsing and facets

`Browse` and `Search` take `facets: true` to ask for counts per filter value; the response then has `facets` when the backend has them (an engine), and none with PostgreSQL. Genre and tag values are tag keys (`genre/fantasy`, `tag/settings/space`); the others are the filters' short names (`manga`, `releasing`, `ja`).

Browsing (no search text) is served by PostgreSQL unless facets are asked for (`search.browse: facets`, the default). `engine` sends every browse to the engine; `postgres` never does.

## Differences between backends

The same conformance suite runs on all three backends: the same filters, sorts, paging, multilingual searches and exclusions of merged and removed entries. Relevance order differs in detail, since each ranks matches its own way. Other differences:

- **Meilisearch** matches word prefixes, not text inside a word ("ntern" doesn't find "Lantern"). It counts at most 20,000 matches of a query (PostgreSQL considers the best 1,000 title and 1,000 name matches). Searches with text and a sort other than relevance are ordered by relevance first, then by the sort.
- **Typesense** splits a query into words with the locale of the field it searches first, so meta leads with the Japanese, Korean or Chinese titles when the query is written in those scripts. A query of Han characters only (Japanese or Chinese) runs as a Japanese and a Chinese search whose matches are merged; such a query pages at most 250 matches deep.
- **Meilisearch** splits a query of Han characters only as Japanese, which in practice also finds Chinese titles.

## Benchmarks

Synthetic catalogue of 300,000 active entries (5 titles, 2 people and 10 tags each), on one laptop (Docker with 8 GB of RAM). Query times are the median and 95th percentile of 40 runs.

Index build (rebuild from PostgreSQL), engine size:

| | Build | Size |
|---|---|---|
| Typesense | 57 s | 810 MB of RAM (index in memory) |
| Meilisearch | 6 min 24 s | 2.9 GB on disk (2.0 GB used) |

Query latency, p50 / p95 (24 hits per page):

| Query | PostgreSQL | Typesense | Meilisearch |
|---|---|---|---|
| Exact title (2–5 words) | 867 / 997 ms | 6.0 / 14 ms | 3.4 / 6.3 ms |
| Fragment of a title (no first letter) | 467 / 489 ms | 1.9 / 4.5 ms | 1.2 / 2.7 ms (no match: prefixes only) |
| One short word | 1,533 / 1,611 ms | 8.4 / 11 ms | 4.6 / 7.6 ms |
| A typo | 1,219 / 1,345 ms | 7.6 / 9.1 ms | 8.0 / 8.8 ms |
| A person's name | 1,214 / 1,312 ms | 4.7 / 11 ms | 4.0 / 4.5 ms |
| One word, type and language filters | 1,538 / 1,612 ms | 11 / 17 ms | 4.0 / 4.9 ms |
| Browse a genre, with facet counts | 8.5 / 9.7 ms (no counts) | 6.7 / 8.0 ms | 15 / 19 ms |

Ingesting the 300,000 records took 4 min 20 s. The numbers of matches differ between backends: each decides differently what is similar enough. PostgreSQL ran in a container on the same laptop with default settings.

The synthetic titles are made-up Latin syllables, so the numbers say nothing about Japanese, Chinese or Korean quality; the conformance suite covers that with real scripts.

## For contributors

- `internal/search` has the `Index` interface, the PostgreSQL backend, the engine adapters (`typesense.go`, `meilisearch.go`: small REST clients rather than the vendors' Go clients, whose releases lag the servers), the document shape (`document.go`), the outbox indexer and the rebuild.
- `internal/search/searchtest` is the conformance suite. Engine tests are opt-in: `META_TEST_ENGINES=1 go test ./internal/search/...` starts the engines with testcontainers, or point `META_TEST_TYPESENSE_URL` / `META_TEST_MEILISEARCH_URL` (keys in `META_TEST_TYPESENSE_KEY` / `META_TEST_MEILISEARCH_KEY`) at running ones, as CI does.
- `internal/searchsvc` chooses the backend from the settings and switches it while meta runs.
- Bump `search.SchemaVersion` when the document or an engine's index settings change: every engine index is rebuilt on the next start.
