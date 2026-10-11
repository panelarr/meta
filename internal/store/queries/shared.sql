-- SPDX-License-Identifier: GPL-3.0-or-later

-- name: FindPersonByExternalIDs :one
SELECT person_id FROM person_external_ids
WHERE (provider, external_id) IN (
    SELECT unnest(@providers::text[]), unnest(@external_ids::text[])
)
LIMIT 1;

-- name: FindNameOnlyPerson :one
SELECT id FROM persons WHERE name_key = @name_key AND NOT has_ids;

-- name: InsertPerson :exec
INSERT INTO persons (id, name, name_key, has_ids, data) VALUES (@id, @name, @name_key, @has_ids, @data)
ON CONFLICT DO NOTHING;

-- name: UpdatePerson :exec
UPDATE persons SET name = @name, name_key = @name_key, data = @data, updated_at = now() WHERE id = @id;

-- name: InsertPersonExternalID :exec
INSERT INTO person_external_ids (provider, external_id, person_id) VALUES (@provider, @external_id, @person_id)
ON CONFLICT DO NOTHING;

-- name: FindCompanyByExternalIDs :one
SELECT company_id FROM company_external_ids
WHERE (provider, external_id) IN (
    SELECT unnest(@providers::text[]), unnest(@external_ids::text[])
)
LIMIT 1;

-- name: FindNameOnlyCompany :one
SELECT id FROM companies WHERE name_key = @name_key AND NOT has_ids;

-- name: InsertCompany :exec
INSERT INTO companies (id, name, name_key, has_ids, data) VALUES (@id, @name, @name_key, @has_ids, @data)
ON CONFLICT DO NOTHING;

-- name: UpdateCompany :exec
UPDATE companies SET name = @name, name_key = @name_key, data = @data, updated_at = now() WHERE id = @id;

-- name: InsertCompanyExternalID :exec
INSERT INTO company_external_ids (provider, external_id, company_id) VALUES (@provider, @external_id, @company_id)
ON CONFLICT DO NOTHING;

-- name: FindTagByExternalID :one
SELECT tag_id FROM tag_external_ids WHERE provider = @provider AND external_id = @external_id;

-- name: UpsertTag :one
INSERT INTO tags (id, name, name_key, description, parent_key, level, is_genre, is_spoiler, is_explicit, content_rating, data)
VALUES (@id, @name, @name_key, @description, @parent_key, @level, @is_genre, @is_spoiler, @is_explicit, @content_rating, @data)
ON CONFLICT (name_key) DO UPDATE
SET name = EXCLUDED.name,
    description = CASE WHEN EXCLUDED.description <> '' THEN EXCLUDED.description ELSE tags.description END,
    parent_key = CASE WHEN EXCLUDED.parent_key <> '' THEN EXCLUDED.parent_key ELSE tags.parent_key END,
    level = GREATEST(EXCLUDED.level, tags.level),
    is_genre = EXCLUDED.is_genre OR tags.is_genre,
    is_spoiler = EXCLUDED.is_spoiler,
    is_explicit = EXCLUDED.is_explicit,
    content_rating = CASE WHEN EXCLUDED.content_rating <> 'unspecified' THEN EXCLUDED.content_rating ELSE tags.content_rating END,
    data = EXCLUDED.data,
    updated_at = now()
WHERE (tags.name, tags.description, tags.parent_key, tags.level, tags.is_genre, tags.is_spoiler, tags.is_explicit, tags.content_rating, tags.data)
   IS DISTINCT FROM
      (EXCLUDED.name,
       CASE WHEN EXCLUDED.description <> '' THEN EXCLUDED.description ELSE tags.description END,
       CASE WHEN EXCLUDED.parent_key <> '' THEN EXCLUDED.parent_key ELSE tags.parent_key END,
       GREATEST(EXCLUDED.level, tags.level), EXCLUDED.is_genre OR tags.is_genre, EXCLUDED.is_spoiler, EXCLUDED.is_explicit,
       CASE WHEN EXCLUDED.content_rating <> 'unspecified' THEN EXCLUDED.content_rating ELSE tags.content_rating END,
       EXCLUDED.data)
RETURNING id;

-- name: GetTagIDByKey :one
SELECT id FROM tags WHERE name_key = @name_key;

-- name: InsertTagExternalID :exec
INSERT INTO tag_external_ids (provider, external_id, tag_id) VALUES (@provider, @external_id, @tag_id)
ON CONFLICT DO NOTHING;

-- name: RecountTags :exec
UPDATE tags t
SET entry_count = c.n
FROM (
    SELECT t2.id, count(e.id)::bigint AS n
    FROM tags t2
    LEFT JOIN entry_tags et ON et.tag_id = t2.id
    LEFT JOIN entries e ON e.id = et.entry_id AND e.state = 'active'
    GROUP BY t2.id
) c
WHERE t.id = c.id AND t.entry_count IS DISTINCT FROM c.n;

-- name: ListTags :many
SELECT * FROM tags
WHERE (NOT @genres_only::boolean OR is_genre)
  AND (@prefix::text = '' OR name_key LIKE @prefix::text || '%')
ORDER BY entry_count DESC, name_key
LIMIT @lim OFFSET @off;
