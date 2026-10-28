-- What Sidekick knows about each Slack user it checked a role for. Until
-- web UI login exists, a user's role comes from slack.roleUsers and from
-- the slack.roleGroups user groups they are in; group membership costs one
-- Slack call per group, so it is kept here and refreshed daily, or at once
-- when a user is refused for their role.

-- +goose Up
CREATE TABLE slack_identities (
  slack_user_id text PRIMARY KEY,
  team_id       text,
  -- For matching the user to a web UI user once login exists.
  email         text,
  user_id       uuid,
  -- The slack.roleGroups user groups the user was in at refreshed_at.
  groups        text[] NOT NULL DEFAULT '{}',
  -- The role the user had at refreshed_at, for display. Lookups work it
  -- out again from groups and the current config.
  role          text,
  refreshed_at  timestamptz NOT NULL
);

-- +goose Down
DROP TABLE slack_identities;
