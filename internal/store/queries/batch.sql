-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Set-based variants used by the catalog writer, so a batch of records
-- costs a handful of round trips instead of several per record.

-- name: GetEntries :many
SELECT * FROM entries WHERE id = ANY(@ids::uuid[]);

-- name: ListProviderEntriesForEntries :many
SELECT * FROM provider_entries WHERE entry_id = ANY(@ids::uuid[]) ORDER BY entry_id, provider, external_id;

-- name: ListFieldLocksForEntries :many
SELECT * FROM field_locks WHERE entry_id = ANY(@ids::uuid[]);

-- name: InsertEntryBatch :batchexec
INSERT INTO entries (id) VALUES (@id);

-- name: UpsertProviderEntryBatch :batchexec
INSERT INTO provider_entries (
    provider, external_id, entry_id, state, merged_into, record, raw, content_hash,
    attribution, provider_updated_at, fetched_at, last_seen_run, removed_at, updated_at
) VALUES (
    @provider, @external_id, @entry_id, @state, @merged_into, @record, @raw, @content_hash,
    @attribution, @provider_updated_at, now(), @last_seen_run,
    CASE WHEN @state::text = 'removed' THEN now() END, now()
)
ON CONFLICT (provider, external_id) DO UPDATE
SET entry_id = COALESCE(EXCLUDED.entry_id, provider_entries.entry_id),
    state = EXCLUDED.state,
    merged_into = EXCLUDED.merged_into,
    record = EXCLUDED.record,
    raw = EXCLUDED.raw,
    content_hash = EXCLUDED.content_hash,
    attribution = EXCLUDED.attribution,
    provider_updated_at = EXCLUDED.provider_updated_at,
    fetched_at = now(),
    last_seen_run = EXCLUDED.last_seen_run,
    removed_at = CASE WHEN EXCLUDED.state = 'removed' THEN COALESCE(provider_entries.removed_at, now()) END,
    updated_at = now();

-- name: UpdateEntryBatch :batchexec
UPDATE entries
SET state = @state,
    merged_into = @merged_into,
    title = @title,
    type = @type,
    raw_type = @raw_type,
    status = @status,
    raw_status = @raw_status,
    content_rating = @content_rating,
    raw_content_rating = @raw_content_rating,
    original_language = @original_language,
    year = @year,
    rating = @rating,
    popularity = @popularity,
    popularity_rank = @popularity_rank,
    cover = @cover,
    dates = @dates,
    progress = @progress,
    adaptations = @adaptations,
    items = @items,
    licenses = @licenses,
    stats = @stats,
    field_sources = @field_sources,
    search_vector = to_tsvector('simple', @search_text::text),
    content_hash = @content_hash,
    updated_at = now()
WHERE id = @id;

-- name: DeleteTitlesOf :exec
DELETE FROM entry_titles WHERE entry_id = ANY(@ids::uuid[]);

-- name: DeleteDescriptionsOf :exec
DELETE FROM entry_descriptions WHERE entry_id = ANY(@ids::uuid[]);

-- name: DeleteCreditsOf :exec
DELETE FROM entry_credits WHERE entry_id = ANY(@ids::uuid[]);

-- name: DeleteCompaniesOf :exec
DELETE FROM entry_companies WHERE entry_id = ANY(@ids::uuid[]);

-- name: DeleteTagsOf :exec
DELETE FROM entry_tags WHERE entry_id = ANY(@ids::uuid[]);

-- name: DeleteRelationsOf :exec
DELETE FROM entry_relations WHERE entry_id = ANY(@ids::uuid[]);

-- name: DeleteImagesOf :exec
DELETE FROM entry_images WHERE entry_id = ANY(@ids::uuid[]);

-- name: DeleteLinksOf :exec
DELETE FROM entry_links WHERE entry_id = ANY(@ids::uuid[]);

-- name: DeleteExternalIDsOf :exec
DELETE FROM entry_external_ids WHERE entry_id = ANY(@ids::uuid[]);

-- name: UpsertNameOnlyPerson :one
INSERT INTO persons (id, name, name_key, has_ids, data) VALUES (@id, @name, @name_key, false, @data)
ON CONFLICT (name_key) WHERE NOT has_ids DO UPDATE SET name_key = EXCLUDED.name_key
RETURNING id;

-- name: UpsertNameOnlyCompany :one
INSERT INTO companies (id, name, name_key, has_ids, data) VALUES (@id, @name, @name_key, false, @data)
ON CONFLICT (name_key) WHERE NOT has_ids DO UPDATE SET name_key = EXCLUDED.name_key
RETURNING id;

-- name: LinkMergedRecords :execrows
-- Records a provider merged, and that meta never gave an entry of their own,
-- point at their target's entry. One statement for the whole provider;
-- repeat to follow chains.
UPDATE provider_entries m
SET entry_id = t.entry_id, updated_at = now()
FROM provider_entries t
WHERE m.provider = @provider
  AND m.state = 'merged'
  AND m.entry_id IS NULL
  AND t.provider = m.provider
  AND t.external_id = m.merged_into
  AND t.entry_id IS NOT NULL;

-- name: FindEntriesForRefs :many
-- Entries carrying any of the given IDs. The caller matches exact
-- (provider, id) pairs; the two arrays only narrow the index scan.
SELECT x.provider, x.external_id, x.entry_id, e.state
FROM entry_external_ids x
JOIN entries e ON e.id = x.entry_id
WHERE x.provider = ANY(@providers::text[]) AND x.external_id = ANY(@external_ids::text[]);

-- name: UpsertNameOnlyPersons :many
INSERT INTO persons (id, name, name_key, has_ids, data)
SELECT unnest(@ids::uuid[]), unnest(@names::text[]), unnest(@name_keys::text[]), false, '{}'::jsonb
ON CONFLICT (name_key) WHERE NOT has_ids DO UPDATE SET name_key = EXCLUDED.name_key
RETURNING id, name_key;

-- name: ListActiveEntryIDsAfter :many
SELECT id FROM entries WHERE state = 'active' AND id > @after ORDER BY id LIMIT @lim;

-- name: PendingMaintenance :many
SELECT * FROM maintenance_jobs ORDER BY created_at;

-- name: FinishMaintenance :exec
DELETE FROM maintenance_jobs WHERE name = @name;
