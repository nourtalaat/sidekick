-- Stopping a run and confirming its verdict. A responder's Stop sets
-- cancel_requested_at, and the worker that owns the run ends it as
-- cancelled. A responder who marks the verdict correct confirms it: the
-- confirmed_* columns hold the verdict people agree with, and later
-- feedback that disagrees changes them.
--
-- A Slack user has no Sidekick user ID until they log in to the web UI,
-- so who stopped or confirmed is kept as a Slack user ID as well as the
-- user ID; at least one of each pair is set when the action happened.

-- +goose Up
ALTER TABLE investigations
  ADD COLUMN cancel_requested_at     timestamptz,
  ADD COLUMN cancel_requested_by     uuid,
  ADD COLUMN cancel_requested_by_slack_user text,
  ADD COLUMN confirmed_verdict       text,
  ADD COLUMN confirmed_cause         text,
  ADD COLUMN confirmed_by            uuid,
  ADD COLUMN confirmed_by_slack_user text,
  ADD COLUMN confirmed_at            timestamptz;

-- +goose Down
ALTER TABLE investigations
  DROP COLUMN cancel_requested_at,
  DROP COLUMN cancel_requested_by,
  DROP COLUMN cancel_requested_by_slack_user,
  DROP COLUMN confirmed_verdict,
  DROP COLUMN confirmed_cause,
  DROP COLUMN confirmed_by,
  DROP COLUMN confirmed_by_slack_user,
  DROP COLUMN confirmed_at;
