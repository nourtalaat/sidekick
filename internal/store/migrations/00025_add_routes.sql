-- Runtime routes, set from Slack with /sidekick route set. Routes in the
-- config file are not stored here; a file route wins over a runtime one of
-- the same name. Every replica reloads them on NOTIFY rules_changed.

-- +goose Up
CREATE TABLE routes (
  name       text PRIMARY KEY,
  -- Lower first; ties go to file routes, then by name.
  priority   int NOT NULL DEFAULT 100,
  -- The rest of the route, in the shape of a file route: match, strategy,
  -- sources, mode, outputs, budgets and grouping.
  spec       jsonb NOT NULL,
  enabled    boolean NOT NULL DEFAULT true,
  -- 1 when created, one more on every change.
  version    int NOT NULL,
  -- Who changed it last, for example a Slack user ID.
  updated_by text,
  updated_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE routes;
