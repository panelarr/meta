-- SPDX-License-Identifier: GPL-3.0-or-later

-- name: ListSolvers :many
SELECT * FROM solvers ORDER BY id;

-- name: GetSolver :one
SELECT * FROM solvers WHERE id = @id;

-- name: PutSolver :exec
INSERT INTO solvers (id, settings) VALUES (@id, @settings)
ON CONFLICT (id) DO UPDATE SET settings = EXCLUDED.settings, updated_at = now();

-- name: DeleteSolver :execrows
DELETE FROM solvers WHERE id = @id;

-- name: ProfilesUsingSolver :many
SELECT id FROM network_profiles WHERE solver_id = @solver_id ORDER BY id;

-- name: CountProfiles :one
SELECT count(*)::bigint FROM network_profiles;

-- name: ListProfiles :many
SELECT * FROM network_profiles ORDER BY id;

-- name: GetProfile :one
SELECT * FROM network_profiles WHERE id = @id;

-- name: PutProfile :exec
INSERT INTO network_profiles (id, solver_id, settings, proxy_password_sealed)
VALUES (@id, @solver_id, @settings, @proxy_password_sealed)
ON CONFLICT (id) DO UPDATE
SET solver_id = EXCLUDED.solver_id,
    settings = EXCLUDED.settings,
    proxy_password_sealed = EXCLUDED.proxy_password_sealed,
    updated_at = now();

-- name: DeleteProfile :execrows
DELETE FROM network_profiles WHERE id = @id;

-- name: GetDefaultProfileID :one
SELECT default_profile_id FROM integration_settings WHERE id = 1;

-- name: SetDefaultProfileID :exec
UPDATE integration_settings SET default_profile_id = @profile_id WHERE id = 1;

-- name: ListAssignments :many
SELECT * FROM profile_assignments ORDER BY provider;

-- name: SetAssignment :exec
INSERT INTO profile_assignments (provider, profile_id) VALUES (@provider, @profile_id)
ON CONFLICT (provider) DO UPDATE SET profile_id = EXCLUDED.profile_id;

-- name: DeleteAssignment :exec
DELETE FROM profile_assignments WHERE provider = @provider;

-- name: ListPushedProfiles :many
SELECT * FROM pushed_profiles ORDER BY provider;

-- name: SetPushedProfile :exec
INSERT INTO pushed_profiles (provider, profile, proxy_password_sealed)
VALUES (@provider, @profile, @proxy_password_sealed)
ON CONFLICT (provider) DO UPDATE
SET profile = EXCLUDED.profile, proxy_password_sealed = EXCLUDED.proxy_password_sealed, updated_at = now();

-- name: DeletePushedProfile :exec
DELETE FROM pushed_profiles WHERE provider = @provider;
