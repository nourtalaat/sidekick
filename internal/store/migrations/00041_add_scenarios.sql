-- Benchmark scenarios built from confirmed investigations. A draft is
-- cleaned of sensitive data by the exporter; an admin reviews it, edits it
-- and ticks "I checked this for sensitive data" before it is marked
-- reviewed. Only reviewed scenarios can be run or downloaded.
--
-- The row holds the cleaned scenario file and nothing of the original
-- data. The review page reads the original from the investigation's events
-- and steps while the retention job still keeps them.

-- +goose Up
CREATE TABLE scenarios (
  -- The id of the scenario file.
  id                 text PRIMARY KEY,
  -- Set to NULL when the retention job deletes the investigation, so the
  -- scenario outlives it. One scenario per investigation.
  from_investigation uuid REFERENCES investigations (id) ON DELETE SET NULL,
  title              text NOT NULL,
  -- draft or reviewed.
  status             text NOT NULL,
  -- The cleaned scenario file, as YAML.
  body               text NOT NULL,
  -- Counts the edits, so a reviewer cannot review text that someone else
  -- changed meanwhile.
  revision           int NOT NULL DEFAULT 1,
  -- Which step each fixture came from, as
  -- [{"source": "signoz", "fixture": 0, "seq": 14}]. Numbers and names
  -- only.
  provenance         jsonb NOT NULL,
  -- What the exporter skipped, merged or could not classify, as
  -- [{"kind": "skipped_call", "text": "..."}], without values.
  notes              jsonb NOT NULL,
  -- NULL when the command line built the draft.
  created_by         uuid REFERENCES users (id),
  created_at         timestamptz NOT NULL,
  reviewed_by        uuid REFERENCES users (id),
  reviewed_at        timestamptz,
  updated_at         timestamptz NOT NULL,
  CONSTRAINT scenarios_id CHECK (id ~ '^[a-z0-9][a-z0-9._-]{0,99}$'),
  CONSTRAINT scenarios_status CHECK (status IN ('draft', 'reviewed')),
  CONSTRAINT scenarios_reviewed CHECK ((status = 'reviewed') = (reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL)),
  CONSTRAINT scenarios_investigation UNIQUE (from_investigation)
);

-- For the list of all scenarios and the list of one status, newest first.
CREATE INDEX scenarios_updated ON scenarios (updated_at DESC, id DESC);
CREATE INDEX scenarios_status_updated ON scenarios (status, updated_at DESC, id DESC);

-- +goose Down
DROP TABLE scenarios;
