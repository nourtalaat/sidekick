-- The dashboard's rollup tables and the triggers that mark their buckets
-- for a recount.
--
-- stats_hourly counts investigations by the hour they were created, with
-- their route, service, model and verdict, and what they cost, how long
-- they took, and what their watchers measured. quality_daily is the
-- confusion matrix of the verdict Sidekick gave against the verdict people
-- confirmed, by the day the investigation was created. Both use UTC hours
-- and days. A bucket is the start of the hour or day, as a timestamp.
--
-- rollup_dirty lists the buckets whose rows are out of date. The rollup job
-- takes them, recounts them from the raw tables and replaces their rows.
--
-- Triggers mark the buckets, in the transaction of the change, so every
-- code path that changes a counted value is covered, including ones added
-- later. What each one watches:
--
--   investigations, on insert
--     Marks the hour: a new investigation is counted in the volume.
--   investigations, on update of route, service, model, strategy, verdict,
--   run, status, started_at, first_finding_at, finished_at,
--   confirmed_verdict or fix_applied_at
--     Marks the hour of stats_hourly when a dimension, the time to first
--     finding, the duration or the fix mark changes, and the day of
--     quality_daily when the verdict, the confirmed verdict, the run or a
--     dimension changes. A run that ends always sets finished_at, so the
--     cost, tokens and tool calls it added are counted by that update.
--   feedback, on insert of a rating of a verdict, or on update of its
--   rating or target
--     Marks the day of quality_daily: a rating of wrong without a real
--     verdict counts, though it does not change the confirmed verdict.
--   watchers, on insert, or on update of status or recovered_at
--     Marks the hour of stats_hourly: recoveries, downtime and timeouts
--     are counted by the investigation's hour.
--
-- tool_calls, cost_micros, input_tokens and output_tokens of an
-- investigation change at every step, so they are left out on purpose: a
-- trigger on them would mark a bucket on every step of every run. The
-- recount reads them when the run ends. If you add a column to a rollup
-- that changes at a time when none of the columns above changes, add it to
-- the trigger of its table, or its bucket will not be recounted.
--
-- Deleting investigations marks nothing, so the dashboard keeps its history
-- after the retention job deletes the investigations.
--
-- A mark updates the bucket's row if it exists, instead of leaving it, so
-- a job that has taken a bucket cannot lose a change that commits after
-- it: the job skips a row that a writer has locked and takes it in the next
-- run.
--
-- The migration marks every hour and day that has investigations, so the
-- first run of the job counts the history. On a large investigations table
-- it reads the table once while the migration runs.

-- +goose Up
ALTER TABLE investigations
  ADD COLUMN fix_applied_at         timestamptz,
  ADD COLUMN fix_applied_by         uuid,
  ADD COLUMN fix_applied_by_slack_user text;

CREATE TABLE stats_hourly (
  hour                 timestamptz NOT NULL,
  -- service and model are '' when the investigation has none, and verdict
  -- is '' while it has none.
  route                text        NOT NULL,
  service              text        NOT NULL,
  model                text        NOT NULL,
  verdict              text        NOT NULL,
  investigations       int         NOT NULL DEFAULT 0,
  -- Investigations that ended: final, failed or cancelled.
  finished             int         NOT NULL DEFAULT 0,
  -- Averages are kept as sums and counts, so any range can be combined.
  duration_n           int         NOT NULL DEFAULT 0,
  duration_ms_sum      bigint      NOT NULL DEFAULT 0,
  first_finding_n      int         NOT NULL DEFAULT 0,
  first_finding_ms_sum bigint      NOT NULL DEFAULT 0,
  -- How many investigations took about how long: 24 counts, bucket 0 for
  -- up to 500 ms and then each bucket up to 1.6 times the one before, so
  -- the dashboard can show an approximate median. See rollup_hist_bucket.
  first_finding_hist   int[]       NOT NULL,
  tool_calls           int         NOT NULL DEFAULT 0,
  cost_micros          bigint      NOT NULL DEFAULT 0,
  input_tokens         bigint      NOT NULL DEFAULT 0,
  output_tokens        bigint      NOT NULL DEFAULT 0,
  -- False positives whose fix a responder marked applied. Merged monitor
  -- PRs are added with the actions.
  noisy_fixed          int         NOT NULL DEFAULT 0,
  actions_proposed     int         NOT NULL DEFAULT 0,
  actions_approved     int         NOT NULL DEFAULT 0,
  actions_executed     int         NOT NULL DEFAULT 0,
  -- Investigations whose watchers all recovered, how long the service was
  -- down from the first event to the last recovery, as a sum and as counts
  -- like first_finding_hist, and watchers that timed out.
  recoveries           int         NOT NULL DEFAULT 0,
  downtime_ms_sum      bigint      NOT NULL DEFAULT 0,
  downtime_hist        int[]       NOT NULL,
  watch_timeouts       int         NOT NULL DEFAULT 0,
  PRIMARY KEY (hour, route, service, model, verdict)
);

CREATE TABLE quality_daily (
  day       date   NOT NULL,
  route     text   NOT NULL,
  service   text   NOT NULL,
  model     text   NOT NULL,
  strategy  text   NOT NULL,
  -- The verdict Sidekick gave, and the one people confirmed. confirmed is
  -- unknown when people rated the verdict wrong without giving the real one.
  predicted text   NOT NULL,
  confirmed text   NOT NULL,
  n         int    NOT NULL,
  PRIMARY KEY (day, route, service, model, strategy, predicted, confirmed)
);

CREATE TABLE rollup_dirty (
  -- stats_hourly or quality_daily.
  table_name text        NOT NULL,
  bucket     timestamptz NOT NULL,
  marked_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (table_name, bucket)
);

-- The histogram bucket of a time in milliseconds, and the counts of a list
-- of such buckets.
-- +goose StatementBegin
CREATE FUNCTION rollup_hist_bucket(ms bigint) RETURNS int
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE
    WHEN ms <= 500 THEN 0
    ELSE LEAST(23, CEIL(ln(ms / 500.0) / ln(1.6))::int)
  END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION rollup_hist(buckets int[]) RETURNS int[]
LANGUAGE sql IMMUTABLE AS $$
  SELECT array_agg((SELECT count(*)::int FROM unnest(buckets) AS b(x) WHERE b.x = k) ORDER BY k)
  FROM generate_series(0, 23) AS k
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION rollup_mark(tbl text, ts timestamptz) RETURNS void
LANGUAGE sql AS $$
  INSERT INTO rollup_dirty (table_name, bucket)
  VALUES (
    tbl,
    CASE tbl
      WHEN 'stats_hourly' THEN date_trunc('hour', ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
      ELSE date_trunc('day', ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
    END
  )
  ON CONFLICT (table_name, bucket) DO UPDATE SET marked_at = now()
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION rollup_investigation_inserted() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM rollup_mark('stats_hourly', NEW.created_at);

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER investigations_rollup_insert
  AFTER INSERT ON investigations
  FOR EACH ROW EXECUTE FUNCTION rollup_investigation_inserted();

-- +goose StatementBegin
CREATE FUNCTION rollup_investigation_updated() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF (OLD.route, OLD.service, OLD.model, OLD.verdict, OLD.status, OLD.started_at, OLD.first_finding_at, OLD.finished_at, OLD.fix_applied_at)
     IS DISTINCT FROM
     (NEW.route, NEW.service, NEW.model, NEW.verdict, NEW.status, NEW.started_at, NEW.first_finding_at, NEW.finished_at, NEW.fix_applied_at)
  THEN
    PERFORM rollup_mark('stats_hourly', NEW.created_at);
  END IF;

  IF (OLD.route, OLD.service, OLD.model, OLD.strategy, OLD.verdict, OLD.run, OLD.confirmed_verdict)
     IS DISTINCT FROM
     (NEW.route, NEW.service, NEW.model, NEW.strategy, NEW.verdict, NEW.run, NEW.confirmed_verdict)
  THEN
    PERFORM rollup_mark('quality_daily', NEW.created_at);
  END IF;

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER investigations_rollup_update
  AFTER UPDATE ON investigations
  FOR EACH ROW
  WHEN (
    OLD.route IS DISTINCT FROM NEW.route
    OR OLD.service IS DISTINCT FROM NEW.service
    OR OLD.model IS DISTINCT FROM NEW.model
    OR OLD.strategy IS DISTINCT FROM NEW.strategy
    OR OLD.verdict IS DISTINCT FROM NEW.verdict
    OR OLD.run IS DISTINCT FROM NEW.run
    OR OLD.status IS DISTINCT FROM NEW.status
    OR OLD.started_at IS DISTINCT FROM NEW.started_at
    OR OLD.first_finding_at IS DISTINCT FROM NEW.first_finding_at
    OR OLD.finished_at IS DISTINCT FROM NEW.finished_at
    OR OLD.confirmed_verdict IS DISTINCT FROM NEW.confirmed_verdict
    OR OLD.fix_applied_at IS DISTINCT FROM NEW.fix_applied_at
  )
  EXECUTE FUNCTION rollup_investigation_updated();

-- +goose StatementBegin
CREATE FUNCTION rollup_feedback_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  created timestamptz;
BEGIN
  SELECT created_at INTO created FROM investigations WHERE id = NEW.investigation_id;

  IF FOUND THEN
    PERFORM rollup_mark('quality_daily', created);
  END IF;

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER feedback_rollup_insert
  AFTER INSERT ON feedback
  FOR EACH ROW
  WHEN (NEW.target_kind = 'verdict')
  EXECUTE FUNCTION rollup_feedback_changed();

CREATE TRIGGER feedback_rollup_update
  AFTER UPDATE ON feedback
  FOR EACH ROW
  WHEN (
    NEW.target_kind = 'verdict'
    AND (OLD.rating IS DISTINCT FROM NEW.rating OR OLD.target_ref IS DISTINCT FROM NEW.target_ref)
  )
  EXECUTE FUNCTION rollup_feedback_changed();

-- +goose StatementBegin
CREATE FUNCTION rollup_watcher_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  created timestamptz;
BEGIN
  SELECT created_at INTO created FROM investigations WHERE group_id = NEW.group_id LIMIT 1;

  IF FOUND THEN
    PERFORM rollup_mark('stats_hourly', created);
  END IF;

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER watchers_rollup_insert
  AFTER INSERT ON watchers
  FOR EACH ROW EXECUTE FUNCTION rollup_watcher_changed();

CREATE TRIGGER watchers_rollup_update
  AFTER UPDATE ON watchers
  FOR EACH ROW
  WHEN (OLD.status IS DISTINCT FROM NEW.status OR OLD.recovered_at IS DISTINCT FROM NEW.recovered_at)
  EXECUTE FUNCTION rollup_watcher_changed();

-- The history that exists before the triggers do.
INSERT INTO rollup_dirty (table_name, bucket)
SELECT DISTINCT 'stats_hourly', date_trunc('hour', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
FROM investigations;

INSERT INTO rollup_dirty (table_name, bucket)
SELECT DISTINCT 'quality_daily', date_trunc('day', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
FROM investigations;

-- For listing benchmark runs, newest first.
CREATE INDEX bench_runs_started ON bench_runs (started_at DESC, id DESC);

-- +goose Down
DROP INDEX bench_runs_started;
DROP TRIGGER watchers_rollup_update ON watchers;
DROP TRIGGER watchers_rollup_insert ON watchers;
DROP FUNCTION rollup_watcher_changed();
DROP TRIGGER feedback_rollup_update ON feedback;
DROP TRIGGER feedback_rollup_insert ON feedback;
DROP FUNCTION rollup_feedback_changed();
DROP TRIGGER investigations_rollup_update ON investigations;
DROP FUNCTION rollup_investigation_updated();
DROP TRIGGER investigations_rollup_insert ON investigations;
DROP FUNCTION rollup_investigation_inserted();
DROP FUNCTION rollup_mark(text, timestamptz);
DROP FUNCTION rollup_hist(int[]);
DROP FUNCTION rollup_hist_bucket(bigint);
DROP TABLE rollup_dirty;
DROP TABLE quality_daily;
DROP TABLE stats_hourly;
ALTER TABLE investigations
  DROP COLUMN fix_applied_by_slack_user,
  DROP COLUMN fix_applied_by,
  DROP COLUMN fix_applied_at;
