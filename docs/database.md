# Database layout

For contributors: how meta's tables map to the [entry schema](schema.md). The schema lives in goose migrations under [`internal/store/migrations`](../internal/store/migrations); queries are plain SQL under [`internal/store/queries`](../internal/store/queries), compiled to Go by sqlc.

## Provider records

`provider_entries` holds each provider's record **as received**: the `ProviderEntry` as protobuf JSON (`record`), the provider's original (`raw`), the attribution, and a `content_hash` used to skip records that didn't change. `entry_id` links it to the merged entry. Records a provider merged or no longer has are kept with `state` `merged` or `removed`, so old links keep working.

## Merged entries

`entries` is meta's merged record, one row per work, with a time-sortable UUIDv7 `id`. The merged view is computed **on write**: whenever a provider record changes, meta rebuilds the entry and its parts. Reads (browse, search, details) never merge at request time.

- Columns used to filter and sort are real columns: `type`, `status`, `content_rating`, `original_language`, `year`, `rating`, `popularity`, `popularity_rank`, `updated_at`, plus a display `title` and a `search_vector`.
- Parts that need joins or indexes have their own tables: `entry_titles` (with a normalized `norm` column and a trigram index), `entry_descriptions`, `entry_credits`, `entry_companies`, `entry_tags`, `entry_relations`, `entry_images`, `entry_links`, `entry_external_ids`.
- Parts that are only ever read with the entry are jsonb columns: `adaptations`, `items`, `licenses`, `dates`, `progress`, `stats` (per provider), and `field_sources` (which provider each field came from).
- Enum values are stored as short lower-case names (`light_novel`, `releasing`), and the provider's raw values next to them.

## Linking and merging

- `entry_external_ids` lists every ID that points at an entry: each source record's own ID and the IDs it reports for other sites. A new provider record joins the entry that shares one of its IDs. Two active records of the **same** provider are never joined this way, because that provider says they are different works.
- When two entries turn out to be the same work, one is retired: `state = 'merged'`, `merged_into` set, and a row in `entry_redirects` so its ID still resolves. Redirects always point straight at the surviving entry.
- `field_locks` pins a field of an entry to a value or to a provider. Locks win over provider priority.

## Shared people, companies and tags

`persons`, `companies` and `tags` are stored once and referenced from entries. Each has an `*_external_ids` table. People and companies known only by name (no IDs) are shared by their normalized name; ones with IDs are kept apart even when the names match. Tags are shared by their `key`, which is made of the kind and the path: `genre/fantasy` for the genre, `tag/settings/fantasy` for the tag "Fantasy" under "Settings". A genre and a tag with the same name, or two tags of the same name under different parents, stay separate. Genres are tags with `is_genre`.

## Maintenance

`maintenance_jobs` holds work a migration leaves for meta to do, such as rebuilding every entry after the merge rules change. `meta serve` runs pending jobs in the background at start (the API answers meanwhile, with the old data until each entry is rebuilt), and `meta rebuild` runs them, or a full rebuild, by hand. A rebuild works from the stored provider records and downloads nothing.

## Syncs

`sync_runs` records every run with its counts, notices, progress and errors. `sync_cursors` keeps where the next run of each provider and ingestion kind continues from.

## Text normalization

Names and titles are normalized in Go (`internal/textnorm`): compatibility forms folded, Latin diacritics removed, lower case, punctuation turned into spaces. The same function runs at write and at search time, so the database needs no extensions beyond `pg_trgm`.
