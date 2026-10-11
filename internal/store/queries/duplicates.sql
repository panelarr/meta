-- SPDX-License-Identifier: GPL-3.0-or-later

-- name: GetJudgeSettings :one
SELECT * FROM judge_settings WHERE id = 1;

-- name: PutJudgeSettings :exec
INSERT INTO judge_settings (id, enabled, kind, url, model, api_key_sealed, covers, updated_at)
VALUES (1, @enabled, @kind, @url, @model, @api_key_sealed, @covers, now())
ON CONFLICT (id) DO UPDATE
SET enabled = EXCLUDED.enabled, kind = EXCLUDED.kind, url = EXCLUDED.url, model = EXCLUDED.model,
    api_key_sealed = EXCLUDED.api_key_sealed, covers = EXCLUDED.covers, updated_at = now();

-- name: DeleteJudgeSettings :exec
DELETE FROM judge_settings WHERE id = 1;

-- name: ListDuplicateCandidates :many
SELECT * FROM duplicate_candidates
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND score >= @min_score
  AND (NOT @judged_only::bool OR verdict IS NOT NULL)
ORDER BY score DESC, id
LIMIT @lim OFFSET @off;

-- name: CountDuplicateCandidates :one
SELECT count(*) FROM duplicate_candidates
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND score >= @min_score
  AND (NOT @judged_only::bool OR verdict IS NOT NULL);

-- name: GetDuplicateCandidate :one
SELECT * FROM duplicate_candidates WHERE id = @id;

-- name: DecideDuplicate :one
UPDATE duplicate_candidates
SET status = @status, decided_at = now(), decided_by = @decided_by, note = @note, updated_at = now()
WHERE id = @id AND status = 'open'
RETURNING *;

-- name: ReopenDuplicate :one
UPDATE duplicate_candidates
SET status = 'open', decided_at = NULL, decided_by = '', note = '', updated_at = now()
WHERE id = @id AND status = 'dismissed'
RETURNING *;

-- name: DropOpenDuplicatesOf :exec
-- Pairs of an entry that just got merged away are moot.
DELETE FROM duplicate_candidates
WHERE status = 'open' AND (entry_a = @entry_id OR entry_b = @entry_id);
