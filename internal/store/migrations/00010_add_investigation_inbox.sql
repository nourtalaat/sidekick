-- Investigation inbox: what arrived for an investigation while it ran, such
-- as a new event in its group or a recovery. The investigation delivers
-- every waiting row as one message before the model's next turn, in the
-- transaction that saves that step, and marks the rows with the step's seq.

-- +goose Up
CREATE TABLE investigation_inbox (
  id               bigserial PRIMARY KEY,
  investigation_id uuid NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
  -- new_event or resolved; follow-ups and watchers add their own kinds.
  kind             text NOT NULL,
  -- For events: {"event_id": "..."}.
  body             jsonb NOT NULL,
  -- The user who wrote it, for follow-ups.
  author           uuid,
  created_at       timestamptz NOT NULL,
  -- The seq of the step that delivered it; NULL while it waits.
  delivered_seq    int
);

CREATE INDEX investigation_inbox_waiting ON investigation_inbox (investigation_id) WHERE delivered_seq IS NULL;

-- +goose Down
DROP TABLE investigation_inbox;
