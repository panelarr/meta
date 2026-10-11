-- SPDX-License-Identifier: GPL-3.0-or-later

-- name: ListClients :many
SELECT * FROM clients ORDER BY created_at, id;

-- name: GetClient :one
SELECT * FROM clients WHERE id = @id;

-- name: GetClientByKeyHash :one
SELECT * FROM clients WHERE key_hash = @key_hash;

-- name: InsertClient :one
INSERT INTO clients (id, name, scopes, key_hash, key_prefix)
VALUES (@id, @name, @scopes, @key_hash, @key_prefix)
RETURNING *;

-- name: UpdateClient :one
UPDATE clients SET name = @name, scopes = @scopes WHERE id = @id RETURNING *;

-- name: RotateClientKey :one
UPDATE clients SET key_hash = @key_hash, key_prefix = @key_prefix, rotated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteClient :execrows
DELETE FROM clients WHERE id = @id;

-- name: TouchClient :exec
UPDATE clients SET last_used_at = @used_at WHERE id = @id;

-- name: GetAdminToken :one
SELECT * FROM admin_token WHERE id = 1;

-- name: SetAdminToken :exec
INSERT INTO admin_token (id, token_hash) VALUES (1, @token_hash)
ON CONFLICT (id) DO UPDATE SET token_hash = EXCLUDED.token_hash, created_at = now();
