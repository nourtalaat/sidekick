-- name: CreateSession :exec
INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, expires_at)
VALUES (@id_hash, @user_id, @csrf_token, @created_at, @expires_at);

-- name: GetSession :one
-- GetSession returns the session with this ID hash, with its user, when
-- it has not expired at now.
SELECT s.csrf_token, s.expires_at, u.id AS user_id, u.name, u.email, u.role
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.id_hash = @id_hash AND s.expires_at > @now;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id_hash = @id_hash;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= @now;
