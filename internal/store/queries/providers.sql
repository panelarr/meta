-- SPDX-License-Identifier: GPL-3.0-or-later

-- name: UpsertProvider :exec
INSERT INTO providers (id, mode, info, health, health_message, updated_at)
VALUES (@id, @mode, @info, @health, @health_message, now())
ON CONFLICT (id) DO UPDATE
SET mode = EXCLUDED.mode,
    info = COALESCE(EXCLUDED.info, providers.info),
    health = EXCLUDED.health,
    health_message = EXCLUDED.health_message,
    updated_at = now();

-- name: GetProvider :one
SELECT * FROM providers WHERE id = @id;

-- name: ListProviders :many
SELECT * FROM providers ORDER BY id;

-- name: CountProviderEntries :many
SELECT provider, count(*)::bigint AS records
FROM provider_entries
GROUP BY provider;
