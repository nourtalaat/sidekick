-- Event groups: the events one investigation looks at. For now the router
-- opens a new group for every event it routes, even when an open group has
-- the same fingerprint; grouping by fingerprint, with a unique index on open
-- groups, comes later.

-- +goose Up
CREATE TABLE event_groups (
  id               uuid PRIMARY KEY,
  fingerprint      text NOT NULL,
  -- The route of the event that opened the group.
  route            text NOT NULL,
  service          text,
  -- open, closed
  status           text NOT NULL,
  event_count      int NOT NULL DEFAULT 1,
  opened_at        timestamptz NOT NULL,
  -- received_at of the newest event in the group.
  last_event_at    timestamptz NOT NULL,
  -- Set once grouping windows exist.
  window_until     timestamptz,
  -- resolved, window_ended; set when the group closes.
  closed_reason    text,
  -- The group's investigation, written in the same transaction.
  investigation_id uuid
);

-- +goose Down
DROP TABLE event_groups;
