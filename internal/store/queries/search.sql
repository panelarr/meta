-- SPDX-License-Identifier: GPL-3.0-or-later

-- name: EnqueueSearch :exec
INSERT INTO search_outbox (entry_id)
SELECT unnest(@ids::uuid[]);

-- name: ClaimSearchOutbox :many
-- The oldest rows, locked until the transaction ends; other indexers skip
-- them.
SELECT seq, entry_id FROM search_outbox
ORDER BY seq
LIMIT @lim
FOR UPDATE SKIP LOCKED;

-- name: DeleteSearchOutbox :exec
DELETE FROM search_outbox WHERE seq = ANY(@seqs::bigint[]);

-- name: TruncateSearchOutboxUpTo :execrows
DELETE FROM search_outbox WHERE seq <= @seq;

-- name: SearchOutboxStats :one
SELECT count(*)::bigint AS pending,
       coalesce(max(seq), 0)::bigint AS last_seq,
       coalesce(min(enqueued_at), now())::timestamptz AS oldest
FROM search_outbox;

-- name: SearchOutboxHead :one
SELECT coalesce(max(seq), 0)::bigint FROM search_outbox;

-- name: ListSearchOutboxAfter :many
SELECT seq, entry_id FROM search_outbox
WHERE seq > @after
ORDER BY seq
LIMIT @lim;

-- name: GetSearchIndex :one
SELECT * FROM search_indexes WHERE backend = @backend;

-- name: SetSearchIndex :exec
INSERT INTO search_indexes (backend, index_name, schema_version, documents, built_at)
VALUES (@backend, @index_name, @schema_version, @documents, now())
ON CONFLICT (backend) DO UPDATE
SET index_name = EXCLUDED.index_name, schema_version = EXCLUDED.schema_version,
    documents = EXCLUDED.documents, built_at = now();

-- name: SetSearchIndexDocuments :exec
UPDATE search_indexes SET documents = @documents WHERE backend = @backend;

-- name: GetSearchSettings :one
SELECT * FROM search_settings WHERE id = 1;

-- name: PutSearchSettings :exec
INSERT INTO search_settings (id, backend, url, api_key_sealed, index_prefix, browse, updated_at)
VALUES (1, @backend, @url, @api_key_sealed, @index_prefix, @browse, now())
ON CONFLICT (id) DO UPDATE
SET backend = EXCLUDED.backend, url = EXCLUDED.url, api_key_sealed = EXCLUDED.api_key_sealed,
    index_prefix = EXCLUDED.index_prefix, browse = EXCLUDED.browse, updated_at = now();

-- name: DeleteSearchSettings :exec
DELETE FROM search_settings WHERE id = 1;

-- name: ForgetSearchIndexesExcept :exec
-- Indexes of engines that aren't fed any more go stale: forget them, so
-- using one again rebuilds it.
DELETE FROM search_indexes WHERE backend <> @backend;

-- name: ClearSearchOutbox :exec
DELETE FROM search_outbox;
