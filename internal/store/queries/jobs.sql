-- SPDX-License-Identifier: GPL-3.0-or-later

-- name: CreateJobRun :one
INSERT INTO job_runs (id, job_id, trigger, status, triggered_by, params, message, finished_at)
VALUES (@id, @job_id, @trigger, @status, @triggered_by, @params, @message, sqlc.narg('finished_at'))
RETURNING *;

-- name: SaveJobRun :exec
UPDATE job_runs
SET counts = @counts, notes = @notes, progress = @progress
WHERE id = @id;

-- name: FinishJobRun :exec
UPDATE job_runs
SET status = @status, message = @message, counts = @counts, notes = @notes,
    progress = NULL, finished_at = now()
WHERE id = @id;

-- name: GetJobRun :one
SELECT * FROM job_runs WHERE id = @id;

-- name: ListJobRuns :many
SELECT * FROM job_runs
WHERE job_id = @job_id
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY started_at DESC, id DESC
LIMIT @lim OFFSET @off;

-- name: LatestJobRuns :many
SELECT DISTINCT ON (job_id) *
FROM job_runs
ORDER BY job_id, started_at DESC;

-- name: RunningJobRun :one
SELECT * FROM job_runs
WHERE job_id = @job_id AND status = 'running'
ORDER BY started_at DESC
LIMIT 1;

-- name: FailInterruptedJobRuns :execrows
-- A run is interrupted when no meta process holds its job's lock any more.
UPDATE job_runs
SET status = 'failed', message = 'interrupted: meta stopped during the run', progress = NULL, finished_at = now()
WHERE status = 'running' AND pg_try_advisory_xact_lock(hashtext('meta-job:' || job_id));

-- name: PruneJobRuns :execrows
-- Keeps the newest @keep finished runs of a job.
DELETE FROM job_runs j
WHERE j.job_id = @job_id AND j.status <> 'running'
  AND j.id NOT IN (
    SELECT k.id FROM job_runs k
    WHERE k.job_id = @job_id AND k.status <> 'running'
    ORDER BY k.started_at DESC
    LIMIT @keep
  );

-- name: ListStaleEntries :many
-- Entries with an active record of one of the providers fetched before
-- @before, the longest unrefreshed first.
SELECT entry_id::uuid AS entry_id, min(fetched_at)::timestamptz AS oldest
FROM provider_entries
WHERE state = 'active' AND entry_id IS NOT NULL
  AND provider = ANY(@providers::text[]) AND fetched_at < @before
GROUP BY entry_id
ORDER BY oldest
LIMIT @lim;
