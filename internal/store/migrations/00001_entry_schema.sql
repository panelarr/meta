-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- The entry schema: provider records as received, merged entries and their
-- parts, shared people/companies/tags, curator locks, merge redirects and
-- sync runs.
--
-- meta needs only its own database. Everything here works for a role that
-- owns the database (or has CREATE on it); pg_trgm is a trusted extension, so
-- no superuser is needed.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Providers meta has seen. The configuration file is the source of truth for
-- which providers exist; this table keeps what they reported and their state.
CREATE TABLE providers (
    id             text PRIMARY KEY,
    mode           text NOT NULL DEFAULT '',
    info           jsonb,
    health         text NOT NULL DEFAULT 'unspecified',
    health_message text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Merged entries: meta's own records. Columns used for browsing, filtering
-- and sorting are real columns; the rest of the merged record lives in parts
-- tables and in jsonb.
CREATE TABLE entries (
    id                 uuid PRIMARY KEY,
    state              text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'merged', 'removed')),
    merged_into        uuid REFERENCES entries (id),
    title              text NOT NULL DEFAULT '',
    type               text NOT NULL DEFAULT 'unspecified',
    raw_type           text NOT NULL DEFAULT '',
    status             text NOT NULL DEFAULT 'unspecified',
    raw_status         text NOT NULL DEFAULT '',
    content_rating     text NOT NULL DEFAULT 'unspecified',
    raw_content_rating text NOT NULL DEFAULT '',
    original_language  text NOT NULL DEFAULT '',
    year               integer,
    -- Sort keys from the preferred provider for each.
    rating             double precision,
    popularity         double precision,
    popularity_rank    bigint,
    cover              jsonb,
    dates              jsonb,
    progress           jsonb,
    -- Less-queried parts of the merged record.
    adaptations        jsonb NOT NULL DEFAULT '[]',
    items              jsonb NOT NULL DEFAULT '[]',
    licenses           jsonb NOT NULL DEFAULT '[]',
    -- Each provider's stats, kept apart: [{provider, stats}].
    stats              jsonb NOT NULL DEFAULT '[]',
    -- Field name -> provider key (or "manual") the value came from.
    field_sources      jsonb NOT NULL DEFAULT '{}',
    -- Full-text document over titles and people, for search.
    search_vector      tsvector,
    -- Hash of the merged content, to skip writes that change nothing.
    content_hash       bytea,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX entries_browse_idx ON entries (state, type, status, content_rating);
CREATE INDEX entries_year_idx ON entries (year) WHERE state = 'active';
CREATE INDEX entries_updated_idx ON entries (updated_at DESC) WHERE state = 'active';
CREATE INDEX entries_rating_idx ON entries (rating DESC NULLS LAST) WHERE state = 'active';
CREATE INDEX entries_popularity_idx ON entries (popularity_rank ASC NULLS LAST) WHERE state = 'active';
CREATE INDEX entries_language_idx ON entries (original_language) WHERE state = 'active';
CREATE INDEX entries_title_idx ON entries (lower(title)) WHERE state = 'active';
CREATE INDEX entries_search_idx ON entries USING gin (search_vector);

-- IDs of retired entries (merged into another) and where they point now.
CREATE TABLE entry_redirects (
    from_id    uuid PRIMARY KEY,
    to_id      uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    reason     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX entry_redirects_to_idx ON entry_redirects (to_id);

-- One provider's record about one item, stored as received. Re-ingesting
-- replaces it.
CREATE TABLE provider_entries (
    provider            text NOT NULL,
    external_id         text NOT NULL,
    entry_id            uuid REFERENCES entries (id) ON DELETE SET NULL,
    state               text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'merged', 'removed')),
    -- The provider's ID this record was merged into (same provider).
    merged_into         text,
    -- The ProviderEntry as protobuf JSON, without raw.
    record              jsonb NOT NULL,
    -- The provider's original record.
    raw                 jsonb,
    -- SHA-256 of the record and raw, to skip re-ingesting unchanged records.
    content_hash        bytea NOT NULL,
    attribution         jsonb,
    provider_updated_at timestamptz,
    fetched_at          timestamptz NOT NULL DEFAULT now(),
    -- The sync run that last delivered or confirmed the record.
    last_seen_run       uuid,
    removed_at          timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, external_id)
);

CREATE INDEX provider_entries_entry_idx ON provider_entries (entry_id);
CREATE INDEX provider_entries_seen_idx ON provider_entries (provider, last_seen_run);

-- External IDs that point at an entry: each source's own ID and the IDs it
-- reports for other sites. Shared IDs link provider records to one entry.
CREATE TABLE entry_external_ids (
    provider    text NOT NULL,
    external_id text NOT NULL,
    entry_id    uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    url         text NOT NULL DEFAULT '',
    -- The provider record that reported the ID.
    source      text NOT NULL DEFAULT '',
    PRIMARY KEY (provider, external_id, entry_id)
);

CREATE INDEX entry_external_ids_entry_idx ON entry_external_ids (entry_id);

-- Shared people. Name-only people (no external IDs) are shared by name.
CREATE TABLE persons (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    name_key   text NOT NULL,
    has_ids    boolean NOT NULL DEFAULT false,
    -- The rest of the Person message as protobuf JSON.
    data       jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX persons_name_only_idx ON persons (name_key) WHERE NOT has_ids;
CREATE INDEX persons_name_trgm_idx ON persons USING gin (name_key gin_trgm_ops);

CREATE TABLE person_external_ids (
    provider    text NOT NULL,
    external_id text NOT NULL,
    person_id   uuid NOT NULL REFERENCES persons (id) ON DELETE CASCADE,
    PRIMARY KEY (provider, external_id)
);

CREATE INDEX person_external_ids_person_idx ON person_external_ids (person_id);

CREATE TABLE companies (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    name_key   text NOT NULL,
    has_ids    boolean NOT NULL DEFAULT false,
    data       jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX companies_name_only_idx ON companies (name_key) WHERE NOT has_ids;

CREATE TABLE company_external_ids (
    provider    text NOT NULL,
    external_id text NOT NULL,
    company_id  uuid NOT NULL REFERENCES companies (id) ON DELETE CASCADE,
    PRIMARY KEY (provider, external_id)
);

CREATE INDEX company_external_ids_company_idx ON company_external_ids (company_id);

-- Shared tags. Genres are tags with is_genre. Tags are shared by name key.
CREATE TABLE tags (
    id             uuid PRIMARY KEY,
    name           text NOT NULL,
    name_key       text NOT NULL UNIQUE,
    description    text NOT NULL DEFAULT '',
    parent_key     text NOT NULL DEFAULT '',
    level          integer NOT NULL DEFAULT 0,
    is_genre       boolean NOT NULL DEFAULT false,
    is_spoiler     boolean NOT NULL DEFAULT false,
    is_explicit    boolean NOT NULL DEFAULT false,
    content_rating text NOT NULL DEFAULT 'unspecified',
    data           jsonb NOT NULL DEFAULT '{}',
    entry_count    bigint NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tags_genre_idx ON tags (is_genre, name_key);

CREATE TABLE tag_external_ids (
    provider    text NOT NULL,
    external_id text NOT NULL,
    tag_id      uuid NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    PRIMARY KEY (provider, external_id)
);

-- Parts of the merged entry. They are rebuilt together whenever the entry
-- changes.
CREATE TABLE entry_titles (
    entry_id   uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    position   integer NOT NULL,
    text       text NOT NULL,
    -- Normalized for matching: lower case, no diacritics, folded width.
    norm       text NOT NULL,
    language   text NOT NULL DEFAULT '',
    kind       text NOT NULL DEFAULT 'unspecified',
    is_primary boolean NOT NULL DEFAULT false,
    traits     text[] NOT NULL DEFAULT '{}',
    note       text NOT NULL DEFAULT '',
    source     text NOT NULL DEFAULT '',
    PRIMARY KEY (entry_id, position)
);

CREATE INDEX entry_titles_norm_trgm_idx ON entry_titles USING gin (norm gin_trgm_ops);
CREATE INDEX entry_titles_language_idx ON entry_titles (language);

CREATE TABLE entry_descriptions (
    entry_id uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    position integer NOT NULL,
    language text NOT NULL DEFAULT '',
    text     text NOT NULL,
    format   text NOT NULL DEFAULT 'unspecified',
    source   text NOT NULL DEFAULT '',
    PRIMARY KEY (entry_id, position)
);

CREATE TABLE entry_credits (
    entry_id  uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    position  integer NOT NULL,
    person_id uuid NOT NULL REFERENCES persons (id),
    role      text NOT NULL DEFAULT 'unspecified',
    raw_role  text NOT NULL DEFAULT '',
    -- Character, order, note, period as protobuf JSON.
    data      jsonb NOT NULL DEFAULT '{}',
    source    text NOT NULL DEFAULT '',
    PRIMARY KEY (entry_id, position)
);

CREATE INDEX entry_credits_person_idx ON entry_credits (person_id);

CREATE TABLE entry_companies (
    entry_id   uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    position   integer NOT NULL,
    company_id uuid NOT NULL REFERENCES companies (id),
    role       text NOT NULL DEFAULT 'unspecified',
    raw_role   text NOT NULL DEFAULT '',
    data       jsonb NOT NULL DEFAULT '{}',
    source     text NOT NULL DEFAULT '',
    PRIMARY KEY (entry_id, position)
);

CREATE INDEX entry_companies_company_idx ON entry_companies (company_id);

CREATE TABLE entry_tags (
    entry_id   uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    tag_id     uuid NOT NULL REFERENCES tags (id),
    position   integer NOT NULL,
    weight     text NOT NULL DEFAULT 'unspecified',
    raw_weight text NOT NULL DEFAULT '',
    score      double precision,
    is_spoiler boolean NOT NULL DEFAULT false,
    source     text NOT NULL DEFAULT '',
    PRIMARY KEY (entry_id, tag_id)
);

CREATE INDEX entry_tags_tag_idx ON entry_tags (tag_id);

CREATE TABLE entry_relations (
    entry_id        uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    position        integer NOT NULL,
    type            text NOT NULL DEFAULT 'unspecified',
    raw_type        text NOT NULL DEFAULT '',
    target_provider text NOT NULL,
    target_id       text NOT NULL,
    title           text NOT NULL DEFAULT '',
    chronology      text NOT NULL DEFAULT 'unspecified',
    manual          boolean NOT NULL DEFAULT false,
    note            text NOT NULL DEFAULT '',
    source          text NOT NULL DEFAULT '',
    PRIMARY KEY (entry_id, position)
);

CREATE INDEX entry_relations_target_idx ON entry_relations (target_provider, target_id);

CREATE TABLE entry_images (
    entry_id uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    position integer NOT NULL,
    kind     text NOT NULL DEFAULT 'unspecified',
    url      text NOT NULL DEFAULT '',
    -- The full Image message as protobuf JSON (variants, placeholders, ...).
    data     jsonb NOT NULL DEFAULT '{}',
    source   text NOT NULL DEFAULT '',
    PRIMARY KEY (entry_id, position)
);

CREATE TABLE entry_links (
    entry_id uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    position integer NOT NULL,
    type     text NOT NULL DEFAULT 'unspecified',
    url      text NOT NULL,
    data     jsonb NOT NULL DEFAULT '{}',
    source   text NOT NULL DEFAULT '',
    PRIMARY KEY (entry_id, position)
);

-- Curator locks: a field pinned to a value or to a provider. Locks win over
-- provider priority when the merged entry is built.
CREATE TABLE field_locks (
    entry_id   uuid NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    field      text NOT NULL,
    -- Pin to a provider's value...
    provider   text,
    -- ...or to a fixed value (protobuf JSON of the field).
    value      jsonb,
    note       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entry_id, field),
    CHECK ((provider IS NULL) <> (value IS NULL))
);

-- Sync runs and their outcome.
CREATE TABLE sync_runs (
    id             uuid PRIMARY KEY,
    provider       text NOT NULL,
    kind           text NOT NULL,
    trigger        text NOT NULL DEFAULT 'manual',
    status         text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled')),
    started_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz,
    snapshot_id    text NOT NULL DEFAULT '',
    full_snapshot  boolean NOT NULL DEFAULT false,
    cursor_start   text NOT NULL DEFAULT '',
    cursor_end     text NOT NULL DEFAULT '',
    upserts        bigint NOT NULL DEFAULT 0,
    changed        bigint NOT NULL DEFAULT 0,
    removals       bigint NOT NULL DEFAULT 0,
    merges         bigint NOT NULL DEFAULT 0,
    skipped        bigint NOT NULL DEFAULT 0,
    marked_removed bigint NOT NULL DEFAULT 0,
    error          text NOT NULL DEFAULT '',
    notices        jsonb NOT NULL DEFAULT '[]',
    progress       jsonb
);

CREATE INDEX sync_runs_provider_idx ON sync_runs (provider, started_at DESC);
CREATE INDEX sync_runs_started_idx ON sync_runs (started_at DESC);

-- Where the next run of each provider and kind continues from.
CREATE TABLE sync_cursors (
    provider    text NOT NULL,
    kind        text NOT NULL,
    cursor      text NOT NULL DEFAULT '',
    snapshot_id text NOT NULL DEFAULT '',
    run_id      uuid,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, kind)
);

-- +goose Down
DROP TABLE sync_cursors;
DROP TABLE sync_runs;
DROP TABLE field_locks;
DROP TABLE entry_links;
DROP TABLE entry_images;
DROP TABLE entry_relations;
DROP TABLE entry_tags;
DROP TABLE entry_companies;
DROP TABLE entry_credits;
DROP TABLE entry_descriptions;
DROP TABLE entry_titles;
DROP TABLE tag_external_ids;
DROP TABLE tags;
DROP TABLE company_external_ids;
DROP TABLE companies;
DROP TABLE person_external_ids;
DROP TABLE persons;
DROP TABLE entry_external_ids;
DROP TABLE provider_entries;
DROP TABLE entry_redirects;
DROP TABLE entries;
DROP TABLE providers;
