-- SPDX-License-Identifier: GPL-3.0-or-later

-- name: InsertEntry :exec
INSERT INTO entries (id) VALUES (@id);

-- name: UpdateEntry :exec
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

-- name: SetEntryState :exec
UPDATE entries SET state = @state, merged_into = @merged_into, updated_at = now() WHERE id = @id;

-- name: GetEntry :one
SELECT * FROM entries WHERE id = @id;

-- name: GetEntryHash :one
SELECT content_hash FROM entries WHERE id = @id;

-- name: DeleteEntry :exec
DELETE FROM entries WHERE id = @id;

-- name: GetRedirect :one
SELECT to_id FROM entry_redirects WHERE from_id = @from_id;

-- name: InsertRedirect :exec
INSERT INTO entry_redirects (from_id, to_id, reason)
VALUES (@from_id, @to_id, @reason)
ON CONFLICT (from_id) DO UPDATE SET to_id = EXCLUDED.to_id, reason = EXCLUDED.reason;

-- name: RepointRedirects :exec
-- Keep redirect chains one hop long.
UPDATE entry_redirects SET to_id = @to_id WHERE to_id = @from_id;

-- name: FindEntriesByExternalIDs :many
-- Entries that any of the given (provider, id) pairs point at.
SELECT DISTINCT x.entry_id, e.state
FROM entry_external_ids x
JOIN entries e ON e.id = x.entry_id
WHERE (x.provider, x.external_id) IN (
    SELECT unnest(@providers::text[]), unnest(@external_ids::text[])
);

-- name: EntryHasProviderRecord :one
-- Whether the entry already holds an active record of the provider (other
-- than the given one). Two records of one provider are never merged by
-- shared IDs: the provider itself keeps them apart.
SELECT EXISTS (
    SELECT 1 FROM provider_entries
    WHERE entry_id = @entry_id AND provider = @provider AND external_id <> @external_id AND state = 'active'
);

-- name: ListEntryExternalIDs :many
SELECT * FROM entry_external_ids WHERE entry_id = @entry_id ORDER BY provider, external_id;

-- name: DeleteEntryExternalIDs :exec
DELETE FROM entry_external_ids WHERE entry_id = @entry_id;

-- name: InsertEntryExternalIDs :copyfrom
INSERT INTO entry_external_ids (provider, external_id, entry_id, url, source)
VALUES (@provider, @external_id, @entry_id, @url, @source);

-- name: LookupExternalID :many
-- Entries an ID points at: through the IDs entries carry, or through the
-- provider record itself (records a provider merged keep pointing at the
-- entry of their target).
SELECT c.entry_id::uuid AS entry_id, c.state::text AS state
FROM (
    SELECT x.entry_id, e.state, e.created_at, 0 AS pri
    FROM entry_external_ids x
    JOIN entries e ON e.id = x.entry_id
    WHERE x.provider = @provider AND x.external_id = @external_id
    UNION ALL
    SELECT e.id, e.state, e.created_at, 1 AS pri
    FROM provider_entries pe
    JOIN entries e ON e.id = pe.entry_id
    WHERE pe.provider = @provider AND pe.external_id = @external_id
) c
GROUP BY c.entry_id, c.state, c.created_at
ORDER BY (c.state = 'active') DESC, min(c.pri), c.created_at;

-- name: DeleteEntryTitles :exec
DELETE FROM entry_titles WHERE entry_id = @entry_id;

-- name: InsertEntryTitles :copyfrom
INSERT INTO entry_titles (entry_id, position, text, norm, language, kind, is_primary, traits, note, source)
VALUES (@entry_id, @position, @text, @norm, @language, @kind, @is_primary, @traits, @note, @source);

-- name: ListEntryTitles :many
SELECT * FROM entry_titles WHERE entry_id = @entry_id ORDER BY position;

-- name: DeleteEntryDescriptions :exec
DELETE FROM entry_descriptions WHERE entry_id = @entry_id;

-- name: InsertEntryDescriptions :copyfrom
INSERT INTO entry_descriptions (entry_id, position, language, text, format, source)
VALUES (@entry_id, @position, @language, @text, @format, @source);

-- name: ListEntryDescriptions :many
SELECT * FROM entry_descriptions WHERE entry_id = @entry_id ORDER BY position;

-- name: DeleteEntryCredits :exec
DELETE FROM entry_credits WHERE entry_id = @entry_id;

-- name: InsertEntryCredits :copyfrom
INSERT INTO entry_credits (entry_id, position, person_id, role, raw_role, data, source)
VALUES (@entry_id, @position, @person_id, @role, @raw_role, @data, @source);

-- name: ListEntryCredits :many
SELECT c.*, p.name AS person_name, p.data AS person_data
FROM entry_credits c
JOIN persons p ON p.id = c.person_id
WHERE c.entry_id = @entry_id
ORDER BY c.position;

-- name: DeleteEntryCompanies :exec
DELETE FROM entry_companies WHERE entry_id = @entry_id;

-- name: InsertEntryCompanies :copyfrom
INSERT INTO entry_companies (entry_id, position, company_id, role, raw_role, data, source)
VALUES (@entry_id, @position, @company_id, @role, @raw_role, @data, @source);

-- name: ListEntryCompanies :many
SELECT c.*, co.name AS company_name, co.data AS company_data
FROM entry_companies c
JOIN companies co ON co.id = c.company_id
WHERE c.entry_id = @entry_id
ORDER BY c.position;

-- name: DeleteEntryTags :exec
DELETE FROM entry_tags WHERE entry_id = @entry_id;

-- name: InsertEntryTags :copyfrom
INSERT INTO entry_tags (entry_id, tag_id, position, weight, raw_weight, score, is_spoiler, source)
VALUES (@entry_id, @tag_id, @position, @weight, @raw_weight, @score, @is_spoiler, @source);

-- name: ListEntryTags :many
SELECT et.*, t.name, t.description, t.parent_key, t.level, t.is_genre, t.is_spoiler AS tag_is_spoiler,
       t.is_explicit, t.content_rating, t.entry_count, t.data AS tag_data, t.key AS tag_key, t.path AS tag_path
FROM entry_tags et
JOIN tags t ON t.id = et.tag_id
WHERE et.entry_id = @entry_id
ORDER BY et.position;

-- name: DeleteEntryRelations :exec
DELETE FROM entry_relations WHERE entry_id = @entry_id;

-- name: InsertEntryRelations :copyfrom
INSERT INTO entry_relations (entry_id, position, type, raw_type, target_provider, target_id, title, chronology, manual, note, source)
VALUES (@entry_id, @position, @type, @raw_type, @target_provider, @target_id, @title, @chronology, @manual, @note, @source);

-- name: ListEntryRelations :many
-- Relations with the meta entry of the target where meta has it.
SELECT r.*, COALESCE(t.id::text, '')::text AS target_entry_id, COALESCE(t.title, '')::text AS target_title
FROM entry_relations r
LEFT JOIN LATERAL (
    SELECT c.id, c.title
    FROM (
        SELECT e.id, e.title, e.state, 0 AS pri
        FROM entry_external_ids x
        JOIN entries e ON e.id = x.entry_id
        WHERE x.provider = r.target_provider AND x.external_id = r.target_id
        UNION ALL
        SELECT e.id, e.title, e.state, 1 AS pri
        FROM provider_entries pe
        JOIN entries e ON e.id = pe.entry_id
        WHERE pe.provider = r.target_provider AND pe.external_id = r.target_id
    ) c
    ORDER BY (c.state = 'active') DESC, c.pri
    LIMIT 1
) t ON true
WHERE r.entry_id = @entry_id
ORDER BY r.position;

-- name: DeleteEntryImages :exec
DELETE FROM entry_images WHERE entry_id = @entry_id;

-- name: InsertEntryImages :copyfrom
INSERT INTO entry_images (entry_id, position, kind, url, data, source)
VALUES (@entry_id, @position, @kind, @url, @data, @source);

-- name: ListEntryImages :many
SELECT * FROM entry_images WHERE entry_id = @entry_id ORDER BY position;

-- name: DeleteEntryLinks :exec
DELETE FROM entry_links WHERE entry_id = @entry_id;

-- name: InsertEntryLinks :copyfrom
INSERT INTO entry_links (entry_id, position, type, url, data, source)
VALUES (@entry_id, @position, @type, @url, @data, @source);

-- name: ListEntryLinks :many
SELECT * FROM entry_links WHERE entry_id = @entry_id ORDER BY position;

-- name: ListFieldLocks :many
SELECT * FROM field_locks WHERE entry_id = @entry_id ORDER BY field;

-- name: SetFieldLock :exec
INSERT INTO field_locks (entry_id, field, provider, value, note)
VALUES (@entry_id, @field, @provider, @value, @note)
ON CONFLICT (entry_id, field) DO UPDATE
SET provider = EXCLUDED.provider, value = EXCLUDED.value, note = EXCLUDED.note;

-- name: DeleteFieldLock :exec
DELETE FROM field_locks WHERE entry_id = @entry_id AND field = @field;

-- name: MoveFieldLocks :exec
-- Carry locks of a retired entry over to the one it was merged into, unless
-- that entry has its own lock for the field.
INSERT INTO field_locks (entry_id, field, provider, value, note, created_at)
SELECT @to_id::uuid, fl.field, fl.provider, fl.value, fl.note, fl.created_at
FROM field_locks fl WHERE fl.entry_id = @from_id::uuid
ON CONFLICT (entry_id, field) DO NOTHING;

-- name: CountEntries :one
SELECT count(*)::bigint FROM entries WHERE state = 'active';
