-- Watcher changes: one row for every state change of a watcher, so the
-- publish job can send each change to every output of the watcher's
-- investigation once, in order. A change takes the seq just below the
-- publish state's new seq, so an output gets the change before the state
-- that shows it. A responder who stops a watcher in Slack is kept as a
-- Slack user ID, since a Slack user has no Sidekick user ID until they
-- log in to the web UI.

-- +goose Up
CREATE TABLE watcher_changes (
  id               bigserial PRIMARY KEY,
  investigation_id uuid NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
  seq              bigint NOT NULL,
  -- The watcher's state after the change, as output.WatcherState JSON.
  state            jsonb NOT NULL,
  -- The outputs the change has been sent to.
  sent_to          text[] NOT NULL DEFAULT '{}',
  created_at       timestamptz NOT NULL
);

CREATE INDEX watcher_changes_investigation ON watcher_changes (investigation_id, seq);

ALTER TABLE watchers ADD COLUMN stopped_by_slack_user text;

-- +goose Down
ALTER TABLE watchers DROP COLUMN stopped_by_slack_user;

DROP TABLE watcher_changes;
