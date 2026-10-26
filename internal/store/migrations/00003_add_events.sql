-- Events: every alert or message that intake accepted, one row each. The
-- (intake, key) pair is unique, so a repeated delivery stores nothing new.
-- Text, labels and the raw payload are redacted before they are stored.

-- +goose Up
CREATE TABLE events (
  id          uuid PRIMARY KEY,
  -- The webhook endpoint or Slack listener name.
  intake      text NOT NULL,
  -- The idempotency key the adapter took from the payload.
  key         text NOT NULL,
  -- alert, resolved, message, mention
  kind        text NOT NULL,
  sender_kind text,
  sender_id   text,
  sender_name text,
  -- From the endpoint's config, never from the payload.
  trusted     boolean NOT NULL,
  alert_name  text,
  title       text,
  body        text,
  service     text,
  severity    text,
  labels      jsonb NOT NULL DEFAULT '{}',
  -- [{"title": ..., "url": ...}], the links the sender attached.
  links       jsonb,
  fingerprint text NOT NULL,
  -- team, channel, ts, thread_ts and user of a Slack message.
  slack       jsonb,
  raw         jsonb,
  -- received, routed, grouped, delayed, skipped
  status      text NOT NULL,
  skip_reason text,
  route       text,
  group_id    uuid,
  starts_at   timestamptz,
  ends_at     timestamptz,
  received_at timestamptz NOT NULL,
  UNIQUE (intake, key)
);

CREATE INDEX events_group ON events (group_id);
CREATE INDEX events_received ON events (received_at);
-- For the rate limits per route.
CREATE INDEX events_route_received ON events (route, received_at);

-- +goose Down
DROP TABLE events;
