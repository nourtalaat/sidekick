-- name: GetSlackIdentity :one
SELECT * FROM slack_identities WHERE slack_user_id = @slack_user_id;

-- name: SaveSlackIdentity :exec
-- SaveSlackIdentity stores the user groups and role found for a Slack
-- user, replacing what was stored before.
INSERT INTO slack_identities (slack_user_id, team_id, groups, role, refreshed_at)
VALUES (@slack_user_id, @team_id, @groups, @role, @refreshed_at)
ON CONFLICT (slack_user_id) DO UPDATE
SET team_id = EXCLUDED.team_id, groups = EXCLUDED.groups, role = EXCLUDED.role, refreshed_at = EXCLUDED.refreshed_at;
