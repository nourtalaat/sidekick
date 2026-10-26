-- Publish states: the newest state of each investigation that its outputs
-- should show. The router writes the first one, "queued", when it creates
-- the investigation; the investigator replaces it as the run goes. Publish
-- jobs read it when they run, so a waiting job always publishes the newest
-- state and older ones are skipped.

-- +goose Up
CREATE TABLE publish_states (
  investigation_id uuid PRIMARY KEY REFERENCES investigations (id) ON DELETE CASCADE,
  -- The outputs of the route, fixed when the investigation is created.
  sinks            text[] NOT NULL,
  -- Increases with every update; an update with a lower or equal seq is
  -- not written. The queued state is 0.
  seq              bigint NOT NULL,
  -- queued, running, final, ...
  phase            text NOT NULL,
  -- The progress lines and the report: {"progress": [...], "report": {...}}.
  body             jsonb NOT NULL,
  updated_at       timestamptz NOT NULL
);

-- +goose Down
DROP TABLE publish_states;
