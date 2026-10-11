-- +goose Up
-- Runs of meta's background jobs (the shared jobs contract). Provider syncs
-- keep their own sync_runs and are shown as jobs from there.
CREATE TABLE job_runs (
    id           uuid PRIMARY KEY,
    job_id       text NOT NULL,
    trigger      text NOT NULL CHECK (trigger IN ('schedule', 'manual', 'startup')),
    status       text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled', 'skipped')),
    started_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz,
    -- One-line outcome, or the error.
    message      text NOT NULL DEFAULT '',
    counts       jsonb NOT NULL DEFAULT '{}',
    -- Who started a manual run: "admin", a client's name or "cli".
    triggered_by text NOT NULL DEFAULT '',
    params       jsonb NOT NULL DEFAULT '{}',
    notes        jsonb NOT NULL DEFAULT '[]',
    progress     jsonb
);

CREATE INDEX job_runs_job_idx ON job_runs (job_id, started_at DESC);
CREATE INDEX job_runs_running_idx ON job_runs (job_id) WHERE status = 'running';

ALTER TABLE sync_runs ADD COLUMN triggered_by text NOT NULL DEFAULT '';
ALTER TABLE sync_runs ADD COLUMN force boolean NOT NULL DEFAULT false;
CREATE INDEX sync_runs_kind_idx ON sync_runs (provider, kind, started_at DESC);

-- +goose Down
DROP INDEX sync_runs_kind_idx;
ALTER TABLE sync_runs DROP COLUMN force;
ALTER TABLE sync_runs DROP COLUMN triggered_by;
DROP TABLE job_runs;
