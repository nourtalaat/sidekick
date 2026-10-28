-- name: GetSlackIdentity :one
SELECT * FROM slack_identities WHERE slack_user_id = @slack_user_id;

-- name: GetSlackIdentityWithUser :one
-- GetSlackIdentityWithUser returns what is stored for a Slack user, with
-- the current role of the web UI user their email matched, if any.
SELECT si.groups, si.email, si.user_id, si.refreshed_at, u.role AS user_role
FROM slack_identities si
LEFT JOIN users u ON u.id = si.user_id
WHERE si.slack_user_id = @slack_user_id;

-- name: FindUserByEmail :one
-- FindUserByEmail returns the web UI user who holds an email, compared
-- without case.
SELECT id, role FROM users WHERE lower(email) = lower(@email::text);

-- name: SaveSlackIdentity :exec
-- SaveSlackIdentity stores the user groups, email, matched web UI user and
-- role found for a Slack user, replacing what was stored before.
INSERT INTO slack_identities (slack_user_id, team_id, email, user_id, groups, role, refreshed_at)
VALUES (@slack_user_id, @team_id, sqlc.narg(email), sqlc.narg(user_id), @groups, @role, @refreshed_at)
ON CONFLICT (slack_user_id) DO UPDATE
SET team_id = EXCLUDED.team_id, email = EXCLUDED.email, user_id = EXCLUDED.user_id, groups = EXCLUDED.groups,
    role = EXCLUDED.role, refreshed_at = EXCLUDED.refreshed_at;
