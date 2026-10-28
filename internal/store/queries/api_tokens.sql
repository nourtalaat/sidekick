-- name: CreateAPIToken :one
INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at)
VALUES (@id, @name, @token_hash, @role, @created_by, @created_at, @expires_at)
RETURNING *;

-- name: GetActiveAPIToken :one
-- GetActiveAPIToken returns the token with this hash when it is neither
-- revoked nor expired at now.
SELECT * FROM api_tokens
WHERE token_hash = @token_hash AND revoked_at IS NULL AND expires_at > @now;

-- name: TouchAPIToken :exec
-- TouchAPIToken sets when a token was last used, unless it was already
-- set after last_used_before, so a busy token is not written every request.
UPDATE api_tokens SET last_used_at = @now::timestamptz
WHERE id = @id AND (last_used_at IS NULL OR last_used_at < @last_used_before::timestamptz);

-- name: ListAPITokens :many
-- ListAPITokens returns every token, newest first. The table holds the
-- few tokens admins create, so it is read whole.
SELECT * FROM api_tokens ORDER BY created_at DESC, id DESC;

-- name: RevokeAPIToken :one
-- RevokeAPIToken revokes a token that is not revoked yet, and returns it.
UPDATE api_tokens SET revoked_at = @now::timestamptz
WHERE id = @id AND revoked_at IS NULL
RETURNING *;
