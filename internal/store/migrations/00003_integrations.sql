-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Network settings for metadata plugins: challenge solvers, network
-- profiles, which profile each provider uses, and profiles pushed by a
-- host. Proxy passwords are stored only encrypted (see internal/secret).

-- +goose Up
CREATE TABLE solvers (
    id         text PRIMARY KEY,
    -- The Solver message as protobuf JSON (without id).
    settings   jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE network_profiles (
    id                    text PRIMARY KEY,
    solver_id             text REFERENCES solvers (id),
    -- The NetworkProfile message as protobuf JSON, without id, solver and
    -- proxy password.
    settings              jsonb NOT NULL,
    proxy_password_sealed text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE profile_assignments (
    provider   text PRIMARY KEY,
    profile_id text NOT NULL REFERENCES network_profiles (id) ON DELETE CASCADE
);

-- One row: integration-wide settings.
CREATE TABLE integration_settings (
    id                 integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    default_profile_id text REFERENCES network_profiles (id) ON DELETE SET NULL
);

INSERT INTO integration_settings (id) VALUES (1);

-- Profiles pushed by a host, in the shape plugins receive them.
CREATE TABLE pushed_profiles (
    provider              text PRIMARY KEY,
    -- metadata.v1 NetworkProfile as protobuf JSON, without the proxy password.
    profile               jsonb NOT NULL,
    proxy_password_sealed text,
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE pushed_profiles;
DROP TABLE integration_settings;
DROP TABLE profile_assignments;
DROP TABLE network_profiles;
DROP TABLE solvers;
