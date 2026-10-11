-- SPDX-License-Identifier: GPL-3.0-or-later

-- name: CreateSyncRun :one
INSERT INTO sync_runs (id, provider, kind, trigger, cursor_start, triggered_by, force)
VALUES (@id, @provider, @kind, @trigger, @cursor_start, @triggered_by, @force)
RETURNING *;

-- name: StartSyncRun :exec
UPDATE sync_runs
SET snapshot_id = @snapshot_id, full_snapshot = @full_snapshot
WHERE id = @id;

-- name: UpdateSyncRunCounts :exec
UPDATE sync_runs
SET upserts = @upserts,
    changed = @changed,
    removals = @removals,
    merges = @merges,
    skipped = @skipped,
    cursor_end = @cursor_end,
    notices = @notices,
    progress = @progress
WHERE id = @id;

-- name: FinishSyncRun :exec
UPDATE sync_runs
SET status = @status,
    error = @error,
    marked_removed = @marked_removed,
    finished_at = now()
WHERE id = @id;

-- name: GetSyncRun :one
SELECT * FROM sync_runs WHERE id = @id;

-- name: ListSyncRuns :many
SELECT * FROM sync_runs
WHERE (sqlc.narg('provider')::text IS NULL OR provider = sqlc.narg('provider'))
ORDER BY started_at DESC, id DESC
LIMIT @lim OFFSET @off;

-- name: ListSyncRunsOfKind :many
SELECT * FROM sync_runs
WHERE provider = @provider AND kind = @kind
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY started_at DESC, id DESC
LIMIT @lim OFFSET @off;

-- name: PruneSyncRuns :execrows
-- Keeps the newest @keep finished runs of a provider and kind.
DELETE FROM sync_runs r
WHERE r.provider = @provider AND r.kind = @kind AND r.status <> 'running'
  AND r.id NOT IN (
    SELECT k.id FROM sync_runs k
    WHERE k.provider = @provider AND k.kind = @kind AND k.status <> 'running'
    ORDER BY k.started_at DESC
    LIMIT @keep
  );

-- name: LatestSyncRuns :many
SELECT DISTINCT ON (provider, kind) *
FROM sync_runs
ORDER BY provider, kind, started_at DESC;

-- name: RunningSyncRun :one
SELECT * FROM sync_runs
WHERE provider = @provider AND status = 'running'
ORDER BY started_at DESC
LIMIT 1;

-- name: FailInterruptedSyncRuns :execrows
UPDATE sync_runs
SET status = 'failed', error = 'interrupted: meta stopped during the run', finished_at = now()
-- Only runs no meta process holds the provider's sync lock for.
WHERE status = 'running' AND pg_try_advisory_xact_lock(hashtext('meta-sync:' || provider));

-- name: GetSyncCursor :one
SELECT * FROM sync_cursors WHERE provider = @provider AND kind = @kind;

-- name: UpsertSyncCursor :exec
INSERT INTO sync_cursors (provider, kind, cursor, snapshot_id, run_id, updated_at)
VALUES (@provider, @kind, @cursor, @snapshot_id, @run_id, now())
ON CONFLICT (provider, kind) DO UPDATE
SET cursor = EXCLUDED.cursor,
    snapshot_id = EXCLUDED.snapshot_id,
    run_id = EXCLUDED.run_id,
    updated_at = now();
