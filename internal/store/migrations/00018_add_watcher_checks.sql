-- Watcher checks: one row for every check a watcher ran, with its result,
-- a short redacted detail and how long it took.

-- +goose Up
CREATE TABLE watcher_checks (
  watcher_id  uuid NOT NULL REFERENCES watchers (id) ON DELETE CASCADE,
  -- 1 for the first check, counting up.
  seq         int NOT NULL,
  at          timestamptz NOT NULL,
  -- pass, fail or error
  result      text NOT NULL,
  -- A redacted summary such as "HTTP 503". It can hold text from the
  -- checked service, so it is untrusted.
  detail      text,
  duration_ms int NOT NULL,
  PRIMARY KEY (watcher_id, seq)
);

-- For the retention job, which deletes old checks.
CREATE INDEX watcher_checks_at ON watcher_checks (at);

-- +goose Down
DROP TABLE watcher_checks;
