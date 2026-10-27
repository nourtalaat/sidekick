-- Watchers: periodic, read-only checks started for an event group that
-- tell when the affected service is back up. Each running watcher has one
-- watch_tick job, which runs a check and schedules the next one. The
-- settings are copied in when the watcher starts, so a config change
-- applies to new watchers only.

-- +goose Up
CREATE TABLE watchers (
  id                uuid PRIMARY KEY,
  group_id          uuid NOT NULL REFERENCES event_groups (id),
  investigation_id  uuid,
  name              text NOT NULL,
  -- route, strategy or model
  origin            text NOT NULL,
  -- The seq of the model turn that called start_watcher, for origin model.
  started_by_seq    int,
  -- http, tool or alert
  check_type        text NOT NULL,
  -- The check, as JSON.
  spec              jsonb NOT NULL,
  interval_s        int NOT NULL,
  passes_to_recover int NOT NULL,
  check_timeout_s   int NOT NULL,
  -- running, recovered, timed_out or stopped; every status but running is
  -- final.
  status            text NOT NULL,
  -- group_resolved, responder or config_changed, for stopped.
  stop_reason       text,
  passes_in_a_row   int NOT NULL DEFAULT 0,
  checks            int NOT NULL DEFAULT 0,
  started_at        timestamptz NOT NULL,
  deadline          timestamptz NOT NULL,
  last_check_at     timestamptz,
  -- When the first check of the current run of passing checks ran; NULL
  -- after a check that did not pass.
  passing_since     timestamptz,
  -- When the first check of the run of passes that recovered the watcher
  -- ran, the end of the downtime it measured.
  recovered_at      timestamptz,
  ended_at          timestamptz,
  stopped_by        uuid,
  CONSTRAINT watchers_group_name UNIQUE (group_id, name)
);

-- For the count of running watchers against watch.maxTotal.
CREATE INDEX watchers_running ON watchers (started_at) WHERE status = 'running';
CREATE INDEX watchers_investigation ON watchers (investigation_id);

-- +goose Down
DROP TABLE watchers;
