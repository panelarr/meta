-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Duplicates: pairs of entries that are probably the same work but aren't
-- linked, found by meta's own comparison and optionally judged by a decision
-- model. A curator confirms (merge) or dismisses each pair; nothing merges
-- on its own.

-- +goose Up

-- Exact title matches are the cheapest and strongest way to find pairs.
CREATE INDEX entry_titles_norm_idx ON entry_titles (norm);

CREATE TABLE duplicate_runs (
    id                uuid PRIMARY KEY,
    trigger           text NOT NULL DEFAULT 'manual',
    status            text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled')),
    started_at        timestamptz NOT NULL DEFAULT now(),
    finished_at       timestamptz,
    -- Entries changed since this time were compared (empty: all of them).
    since             timestamptz,
    entries_scanned   bigint NOT NULL DEFAULT 0,
    pairs_scored      bigint NOT NULL DEFAULT 0,
    candidates_new    bigint NOT NULL DEFAULT 0,
    candidates_open   bigint NOT NULL DEFAULT 0,
    model_calls       bigint NOT NULL DEFAULT 0,
    model_tokens      bigint NOT NULL DEFAULT 0,
    model_cached      bigint NOT NULL DEFAULT 0,
    error             text NOT NULL DEFAULT ''
);

CREATE INDEX duplicate_runs_started_idx ON duplicate_runs (started_at DESC);

CREATE TABLE duplicate_candidates (
    id          bigserial PRIMARY KEY,
    -- The pair, in a fixed order so each pair is stored once.
    entry_a     uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    entry_b     uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    CHECK (entry_a < entry_b),
    status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'confirmed', 'dismissed')),
    -- 0..1: how likely meta's own comparison thinks the pair is one work.
    score       double precision NOT NULL,
    -- What the score is made of: title, people, year, type, language, IDs.
    signals     jsonb NOT NULL,
    -- Hash of both entries' content when they were compared, so a model
    -- verdict is only reused while neither changed.
    pair_hash   bytea NOT NULL,
    -- The decision model's verdict, if one was asked.
    verdict     jsonb,
    run_id      uuid,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    decided_at  timestamptz,
    -- Who decided: "admin" or a client ID.
    decided_by  text NOT NULL DEFAULT '',
    UNIQUE (entry_a, entry_b)
);

CREATE INDEX duplicate_candidates_open_idx ON duplicate_candidates (status, score DESC);
CREATE INDEX duplicate_candidates_b_idx ON duplicate_candidates (entry_b);

-- Model verdicts by pair content and judge, so an unchanged pair is never
-- sent twice.
CREATE TABLE duplicate_verdicts (
    pair_hash  bytea NOT NULL,
    judge      text NOT NULL,
    verdict    jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (pair_hash, judge)
);

-- +goose Down
DROP TABLE duplicate_verdicts;
DROP TABLE duplicate_candidates;
DROP TABLE duplicate_runs;
DROP INDEX entry_titles_norm_idx;
