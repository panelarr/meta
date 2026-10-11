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
LEFT JOIN tags p ON p.name_key = t.parent_key AND t.parent_key <> ''
WHERE (NOT @genres_only::boolean OR t.is_genre)
  AND (@prefix::text = '' OR t.name_key LIKE @prefix::text || '%')
  AND t.entry_count > 0
ORDER BY t.entry_count DESC, t.name_key
LIMIT @lim OFFSET @off;

-- name: ListSourcesForEntry :many
SELECT provider, external_id, state, record, attribution, fetched_at, provider_updated_at
FROM provider_entries
WHERE entry_id = @entry_id
ORDER BY provider, external_id;
