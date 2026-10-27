-- The Microsoft Teams message an event came from, like events.slack for
-- Slack messages.

-- +goose Up
-- tenant, team, channel, message, thread, user and service URL of a Teams
-- message.
ALTER TABLE events ADD COLUMN teams jsonb;

-- +goose Down
ALTER TABLE events DROP COLUMN teams;
