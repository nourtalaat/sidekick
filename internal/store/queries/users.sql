-- name: ReleaseUserEmail :many
-- ReleaseUserEmail clears the email from every other user than the one
-- with this issuer and subject, so that user can take it, and returns
-- their IDs.
UPDATE users SET email = NULL
WHERE lower(email) = lower(@email::text)
  AND NOT (oidc_issuer = @oidc_issuer AND oidc_subject = @oidc_subject)
RETURNING id;

-- name: InsertUser :one
-- InsertUser adds a user at their first login. It returns no row when a
-- user with the issuer and subject exists.
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at)
VALUES (@id, @oidc_issuer, @oidc_subject, sqlc.narg(email), @name, @groups, @role, @now, @now)
ON CONFLICT (oidc_issuer, oidc_subject) DO NOTHING
RETURNING *;

-- name: LockUserBySubject :one
SELECT * FROM users
WHERE oidc_issuer = @oidc_issuer AND oidc_subject = @oidc_subject
FOR UPDATE;

-- name: UpdateUserLogin :one
-- UpdateUserLogin sets what a login found out about a user.
UPDATE users
SET email = sqlc.narg(email), name = @name, groups = @groups, role = @role, last_login_at = @now
WHERE id = @id
RETURNING *;
