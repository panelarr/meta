-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Tags are told apart by kind and path, not by name alone. Before this, a
-- provider's tag "Settings > Fantasy" and the genre "Fantasy" shared one row
-- (and its genre flag), so entries with that tag showed Fantasy as a genre.
--
-- Existing rows get a key from what they were; the entries are then rebuilt
-- from their stored provider records (a maintenance job meta runs at the next
-- start, or `meta rebuild`), which moves each assignment to the right tag.

-- +goose Up
ALTER TABLE tags ADD COLUMN key text;
ALTER TABLE tags ADD COLUMN path text[] NOT NULL DEFAULT '{}';
UPDATE tags SET key = (CASE WHEN is_genre THEN 'genre/' ELSE 'tag/' END) || replace(name_key, ' ', '-');
ALTER TABLE tags ALTER COLUMN key SET NOT NULL;
ALTER TABLE tags DROP CONSTRAINT tags_name_key_key;
CREATE UNIQUE INDEX tags_key_idx ON tags (key);
CREATE INDEX tags_name_key_idx ON tags (name_key);

-- One-off jobs meta runs after an upgrade.
CREATE TABLE maintenance_jobs (
    name       text PRIMARY KEY,
    reason     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO maintenance_jobs (name, reason)
VALUES ('rebuild-entries', 'tags are now told apart by kind and path: rebuild entries so genres and tags are right');

-- +goose Down
DROP TABLE maintenance_jobs;
DROP INDEX tags_name_key_idx;
DROP INDEX tags_key_idx;
ALTER TABLE tags ADD CONSTRAINT tags_name_key_key UNIQUE (name_key);
ALTER TABLE tags DROP COLUMN path;
ALTER TABLE tags DROP COLUMN key;
