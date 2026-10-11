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

-- +goose Down
DROP TABLE search_indexes;
DROP TABLE search_outbox;
