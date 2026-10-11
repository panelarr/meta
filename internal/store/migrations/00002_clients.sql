-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- API clients and the admin token. Keys and tokens are stored only as
-- SHA-256 hashes: they are 256-bit random values, so a slow password hash
-- would add latency to every request without making them harder to guess.

-- +goose Up
CREATE TABLE clients (
    id           text PRIMARY KEY,
    name         text NOT NULL,
    scopes       text[] NOT NULL,
    key_hash     bytea NOT NULL UNIQUE,
    key_prefix   text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    rotated_at   timestamptz,
    last_used_at timestamptz
);

-- The generated admin token (when META_ADMIN_TOKEN isn't set). One row.
CREATE TABLE admin_token (
    id         integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    token_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE admin_token;
DROP TABLE clients;
