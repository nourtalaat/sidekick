-- Runtime listeners, added from Slack with /sidekick listen. Listeners in
-- the config file are not stored here; a file listener wins over a runtime
-- one of the same name. Every replica reloads them on NOTIFY rules_changed.

-- +goose Up
CREATE TABLE listeners (
  name          text PRIMARY KEY,
  -- A channel name with its "#", or a channel ID.
  slack_channel text NOT NULL,
  -- The rest of the listener, in the shape of a file listener:
  -- {"sender": {...}, "text": "...", "extract": [...], "recovery": "..."}.
  match         jsonb NOT NULL,
  trusted       boolean NOT NULL DEFAULT false,
  enabled       boolean NOT NULL DEFAULT true,
  -- 1 when created, one more on every change.
  version       int NOT NULL,
  -- Who changed it last, for example a Slack user ID.
  updated_by    text,
  updated_at    timestamptz NOT NULL
);

-- +goose Down
DROP TABLE listeners;
