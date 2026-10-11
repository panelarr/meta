-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Reads for the API: everything a page of results needs, for many entries
-- at once.

-- name: ListTitlesForEntries :many
SELECT * FROM entry_titles WHERE entry_id = ANY(@ids::uuid[]) ORDER BY entry_id, position;

-- name: ListGenresForEntries :many
SELECT et.entry_id, t.name
FROM entry_tags et
JOIN tags t ON t.id = et.tag_id
WHERE et.entry_id = ANY(@ids::uuid[]) AND t.is_genre
ORDER BY et.entry_id, et.position;

-- name: ListAttributionsForEntries :many
SELECT DISTINCT ON (entry_id, provider) entry_id, provider, attribution
FROM provider_entries
WHERE entry_id = ANY(@ids::uuid[]) AND state = 'active' AND attribution IS NOT NULL
ORDER BY entry_id, provider;

-- name: ListTagsWithParent :many
SELECT t.*, COALESCE(p.name, '')::text AS parent_name
FROM tags t
LEFT JOIN LATERAL (
    SELECT p.name FROM tags p
    WHERE p.name_key = t.parent_key AND t.parent_key <> '' AND p.is_genre = t.is_genre
    LIMIT 1
) p ON true
WHERE (NOT @genres_only::boolean OR t.is_genre)
  AND (NOT @tags_only::boolean OR NOT t.is_genre)
  AND (@prefix::text = '' OR t.name_key LIKE @prefix::text || '%')
  AND (@query::text = '' OR (t.name_key || ' ' || lower(array_to_string(t.path, ' '))) LIKE '%' || @query::text || '%')
  AND t.entry_count > 0
ORDER BY t.is_genre DESC, t.entry_count DESC, t.name_key
LIMIT @lim OFFSET @off;

-- name: ListOriginalLanguages :many
SELECT original_language AS code, count(*)::bigint AS entries
FROM entries
WHERE state = 'active' AND original_language <> ''
GROUP BY original_language
ORDER BY entries DESC, code;

-- name: ListTitleLanguages :many
SELECT t.language AS code, count(DISTINCT t.entry_id)::bigint AS entries
FROM entry_titles t
JOIN entries e ON e.id = t.entry_id AND e.state = 'active'
WHERE t.language <> ''
GROUP BY t.language
ORDER BY entries DESC, code;

-- name: ListGenreKeysForEntries :many
SELECT et.entry_id, t.name, t.key
FROM entry_tags et
JOIN tags t ON t.id = et.tag_id
WHERE et.entry_id = ANY(@ids::uuid[]) AND t.is_genre
ORDER BY et.entry_id, et.position;

-- name: ListSourcesForEntry :many
SELECT provider, external_id, state, record, attribution, fetched_at, provider_updated_at
FROM provider_entries
WHERE entry_id = @entry_id
ORDER BY provider, external_id;
