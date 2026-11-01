-- Runtime strategies, edited in the web UI. Every save is a new row in
-- strategy_versions, so an investigation can refer to the exact version it
-- used, and no row is ever changed or deleted. A strategy in the config
-- file or built into the binary is not stored here; a file strategy wins
-- over a runtime one of the same name, and a runtime one wins over a
-- built-in one. Every replica reloads them on NOTIFY strategies_changed.

-- +goose Up
CREATE TABLE strategies (
  name            text PRIMARY KEY,
  -- The version investigations that start now use. 1 when created, one more
  -- with every save.
  current_version int NOT NULL,
  created_at      timestamptz NOT NULL
);

CREATE TABLE strategy_versions (
  name        text NOT NULL REFERENCES strategies (name),
  version     int NOT NULL,
  -- The strategy as YAML, in the format of a strategy file.
  body        text NOT NULL,
  -- The ID of the web UI user or API token that saved it, or null when
  -- Sidekick did not know who.
  author      uuid,
  reason      text NOT NULL CONSTRAINT strategy_versions_reason CHECK (btrim(reason) <> ''),
  created_at  timestamptz NOT NULL,
  -- Set when a version comes from a strategy proposal and the benchmark
  -- run that checked it. Strategy proposals add the keys to their own
  -- tables.
  proposal_id  uuid,
  bench_run_id uuid,
  -- Lists the versions of a strategy, newest first, and finds one.
  PRIMARY KEY (name, version)
);

-- +goose Down
DROP TABLE strategy_versions;
DROP TABLE strategies;
