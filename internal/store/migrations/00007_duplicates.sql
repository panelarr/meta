-- +goose Up
-- Duplicates: pairs of entries that are probably the same work but aren't
-- linked, found by meta's own comparison (the duplicates job, recorded in
-- job_runs) and optionally judged by a decision model. A curator confirms
-- (merges) or dismisses each pair; nothing merges on its own.

-- Exact title matches are the cheapest way to find pairs.
CREATE INDEX entry_titles_norm_idx ON entry_titles (norm);

CREATE TABLE duplicate_candidates (
    id          bigserial PRIMARY KEY,
    -- The pair, in a fixed order so each pair is stored once.
    entry_a     uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    entry_b     uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    CHECK (entry_a < entry_b),
    status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'confirmed', 'dismissed')),
    -- 0..1: how likely meta's own comparison thinks the pair is one work.
    score       double precision NOT NULL,
    -- What the score is made of: titles, people, year, type, language, IDs.
    signals     jsonb NOT NULL,
    -- Hash of what was compared, so a model verdict is reused only while
    -- neither entry changed.
    pair_hash   bytea NOT NULL,
    -- The decision model's verdict, if one was asked.
    verdict     jsonb,
    -- The job run that found or last updated the pair.
    run_id      uuid,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    decided_at  timestamptz,
    -- Who decided: "admin" or a client's name.
    decided_by  text NOT NULL DEFAULT '',
    note        text NOT NULL DEFAULT '',
    UNIQUE (entry_a, entry_b)
);

CREATE INDEX duplicate_candidates_status_idx ON duplicate_candidates (status, score DESC);
CREATE INDEX duplicate_candidates_b_idx ON duplicate_candidates (entry_b);

-- Model verdicts by the pair's content and the judge, so an unchanged pair
-- is never sent twice.
CREATE TABLE duplicate_verdicts (
    pair_hash  bytea NOT NULL,
    judge      text NOT NULL,
    verdict    jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (pair_hash, judge)
);

-- Decision model settings saved in the admin; they win over the
-- configuration file. The API key is sealed with meta's secret key.
CREATE TABLE judge_settings (
    id             integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled        boolean NOT NULL DEFAULT false,
    kind           text NOT NULL DEFAULT '',
    url            text NOT NULL DEFAULT '',
    model          text NOT NULL DEFAULT '',
    api_key_sealed text,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE judge_settings;
DROP TABLE duplicate_verdicts;
DROP TABLE duplicate_candidates;
DROP INDEX entry_titles_norm_idx;
