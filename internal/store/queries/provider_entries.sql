-- SPDX-License-Identifier: GPL-3.0-or-later

-- name: GetProviderEntryStates :many
-- What meta has for a batch of a provider's records, to skip unchanged ones.
SELECT external_id, content_hash, entry_id, state, merged_into
FROM provider_entries
WHERE provider = @provider AND external_id = ANY(@external_ids::text[]);

-- name: UpsertProviderEntry :exec
INSERT INTO provider_entries (
    provider, external_id, entry_id, state, merged_into, record, raw, content_hash,
    attribution, provider_updated_at, fetched_at, last_seen_run, removed_at, updated_at
) VALUES (
    @provider, @external_id, @entry_id, @state, @merged_into, @record, @raw, @content_hash,
    @attribution, @provider_updated_at, now(), @last_seen_run, NULL, now()
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
    removed_at = NULL,
    updated_at = now();

-- name: TouchProviderEntries :exec
-- Records seen unchanged in a run.
UPDATE provider_entries
SET last_seen_run = @run_id, fetched_at = now()
WHERE provider = @provider AND external_id = ANY(@external_ids::text[]);

-- name: GetProviderEntry :one
SELECT * FROM provider_entries WHERE provider = @provider AND external_id = @external_id;

-- name: ListProviderEntriesForEntry :many
SELECT * FROM provider_entries WHERE entry_id = @entry_id ORDER BY provider, external_id;

-- name: SetProviderEntryEntry :exec
UPDATE provider_entries SET entry_id = @entry_id, updated_at = now()
WHERE provider = @provider AND external_id = @external_id;

-- name: MoveProviderEntries :exec
UPDATE provider_entries SET entry_id = @to_id, updated_at = now()
WHERE entry_id = @from_id;

-- name: SetProviderEntryMerged :one
-- The provider merged this record into another one. Returns the entry the
-- record belongs to, if any.
UPDATE provider_entries
SET state = 'merged', merged_into = @merged_into, last_seen_run = @run_id, updated_at = now()
WHERE provider = @provider AND external_id = @external_id
RETURNING entry_id;

-- name: SetProviderEntryRemoved :one
UPDATE provider_entries
SET state = 'removed', removed_at = COALESCE(removed_at, now()), last_seen_run = @run_id, updated_at = now()
WHERE provider = @provider AND external_id = @external_id
RETURNING entry_id;

-- name: MarkUnseenRemoved :many
-- After a full snapshot: records the run didn't deliver are gone at the
-- provider. They are kept, marked removed. Returns the affected entries.
UPDATE provider_entries
SET state = 'removed', removed_at = now(), updated_at = now()
WHERE provider = @provider
  AND state = 'active'
  AND last_seen_run IS DISTINCT FROM @run_id::uuid
RETURNING entry_id;

-- name: ListMergeTargets :many
-- Provider merges whose records still sit in different entries. With
-- external_ids empty, checks every merged record of the provider.
SELECT m.external_id, m.entry_id AS from_entry, t.entry_id AS to_entry
FROM provider_entries m
JOIN provider_entries t ON t.provider = m.provider AND t.external_id = m.merged_into
WHERE m.provider = @provider
  AND m.state = 'merged'
  AND m.entry_id IS NOT NULL
  AND t.entry_id IS NOT NULL
  AND m.entry_id <> t.entry_id
  AND (cardinality(@external_ids::text[]) = 0 OR m.external_id = ANY(@external_ids::text[]));
