-- +goose Up
-- Entries an external search engine must re-read: written in the same
-- transaction as the entry change, only while such an engine is
-- configured. The indexer reads each entry's current state, so a row only
-- says "look at this entry again".
CREATE TABLE search_outbox (
    seq         bigserial PRIMARY KEY,
    entry_id    uuid NOT NULL,
    enqueued_at timestamptz NOT NULL DEFAULT now()
);

-- The live index of each engine: its physical name, document schema
-- version and when it was built.
CREATE TABLE search_indexes (
    backend        text PRIMARY KEY,
    index_name     text NOT NULL,
    schema_version integer NOT NULL,
    documents      bigint NOT NULL DEFAULT 0,
    built_at       timestamptz NOT NULL DEFAULT now()
);

-- Search engine settings saved in the admin; they win over the
-- configuration file. The API key is sealed with meta's secret key.
CREATE TABLE search_settings (
    id             integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    backend        text NOT NULL,
    url            text NOT NULL DEFAULT '',
    api_key_sealed text,
    index_prefix   text NOT NULL DEFAULT 'meta',
    browse         text NOT NULL DEFAULT 'facets',
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Jobs meta starts on its own while running (e.g. a search rebuild).
ALTER TABLE job_runs DROP CONSTRAINT job_runs_trigger_check;
ALTER TABLE job_runs ADD CONSTRAINT job_runs_trigger_check CHECK (trigger IN ('schedule', 'manual', 'startup', 'system'));

-- +goose Down
DELETE FROM job_runs WHERE trigger = 'system';
ALTER TABLE job_runs DROP CONSTRAINT job_runs_trigger_check;
ALTER TABLE job_runs ADD CONSTRAINT job_runs_trigger_check CHECK (trigger IN ('schedule', 'manual', 'startup'));
DROP TABLE search_settings;
DROP TABLE search_indexes;
DROP TABLE search_outbox;
