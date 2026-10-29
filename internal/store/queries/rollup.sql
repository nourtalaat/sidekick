-- name: TakeDirtyBuckets :many
-- TakeDirtyBuckets deletes up to batch of a table's dirty buckets, newest
-- first, and returns them. A bucket that a writer holds a lock on, because
-- it marks the bucket in a transaction that has not ended, is left for the
-- next run, which sees the change that the mark is for. Run it in the
-- transaction that recounts the buckets: if that fails, the marks come back.
DELETE FROM rollup_dirty
WHERE rollup_dirty.table_name = @table_name
  AND rollup_dirty.bucket IN (
    SELECT d.bucket FROM rollup_dirty d
    WHERE d.table_name = @table_name
    ORDER BY d.bucket DESC
    LIMIT @batch
    FOR UPDATE SKIP LOCKED
  )
RETURNING bucket;

-- name: CountDirtyBuckets :one
-- CountDirtyBuckets returns how many buckets wait for a recount.
SELECT count(*)::bigint FROM rollup_dirty;

-- name: DeleteStatsHourly :exec
-- DeleteStatsHourly deletes the rows of the hours in buckets, which the
-- recount adds again.
DELETE FROM stats_hourly WHERE hour = ANY(@buckets::timestamptz[]);

-- name: DeleteQualityDaily :exec
-- DeleteQualityDaily deletes the rows of the days that start at buckets,
-- which the recount adds again.
DELETE FROM quality_daily
WHERE day = ANY (ARRAY(SELECT (b AT TIME ZONE 'UTC')::date FROM unnest(@buckets::timestamptz[]) AS b));

-- name: RecountStatsHourly :exec
-- RecountStatsHourly counts the investigations created in each hour that
-- starts at one of buckets, by route, service, model and verdict, into
-- stats_hourly. The rows of the hours must be deleted first. An
-- investigation counts in the hour it was created in, whatever happens to
-- it later. Its watchers count once the group's last one has ended: it
-- recovered when some watcher recovered and none is running or timed out,
-- and the downtime runs from the group's first event to the last recovery.
-- The range on created_at uses the index on it.
WITH per AS (
  SELECT b.hour,
         i.route,
         coalesce(i.service, '') AS service,
         coalesce(i.model, '') AS model,
         coalesce(i.verdict, '') AS verdict,
         i.finished_at IS NOT NULL AS finished,
         CASE WHEN i.finished_at IS NOT NULL AND i.started_at IS NOT NULL
              THEN (extract(epoch FROM i.finished_at - i.started_at) * 1000)::bigint END AS duration_ms,
         CASE WHEN i.first_finding_at IS NOT NULL
              THEN (extract(epoch FROM i.first_finding_at - i.created_at) * 1000)::bigint END AS first_finding_ms,
         i.tool_calls,
         i.cost_micros,
         i.input_tokens,
         i.output_tokens,
         (i.verdict = 'false_positive' AND i.fix_applied_at IS NOT NULL) AS noisy_fixed,
         (wt.recovered > 0 AND wt.running = 0 AND wt.timed_out = 0) AS recovered,
         CASE WHEN wt.recovered > 0 AND wt.running = 0 AND wt.timed_out = 0
              THEN greatest(0, (extract(epoch FROM wt.last_recovered_at - fe.first_event_at) * 1000)::bigint) END AS downtime_ms,
         wt.timed_out
  FROM unnest(@buckets::timestamptz[]) AS b(hour)
  JOIN investigations i ON i.created_at >= b.hour AND i.created_at < b.hour + interval '1 hour'
  LEFT JOIN LATERAL (
    SELECT count(*) FILTER (WHERE w.status = 'recovered') AS recovered,
           count(*) FILTER (WHERE w.status = 'running') AS running,
           count(*) FILTER (WHERE w.status = 'timed_out') AS timed_out,
           max(w.recovered_at) FILTER (WHERE w.status = 'recovered') AS last_recovered_at
    FROM watchers w
    WHERE w.group_id = i.group_id
  ) wt ON true
  LEFT JOIN LATERAL (
    SELECT min(coalesce(e.starts_at, e.received_at)) AS first_event_at
    FROM events e
    WHERE e.group_id = i.group_id
  ) fe ON true
)
INSERT INTO stats_hourly (
  hour, route, service, model, verdict,
  investigations, finished,
  duration_n, duration_ms_sum,
  first_finding_n, first_finding_ms_sum, first_finding_hist,
  tool_calls, cost_micros, input_tokens, output_tokens,
  noisy_fixed,
  recoveries, downtime_ms_sum, downtime_hist, watch_timeouts
)
SELECT hour, route, service, model, verdict,
       count(*)::int,
       (count(*) FILTER (WHERE finished))::int,
       count(duration_ms)::int,
       coalesce(sum(duration_ms), 0)::bigint,
       count(first_finding_ms)::int,
       coalesce(sum(first_finding_ms), 0)::bigint,
       rollup_hist((array_agg(rollup_hist_bucket(first_finding_ms)) FILTER (WHERE first_finding_ms IS NOT NULL))::int[]),
       coalesce(sum(tool_calls), 0)::int,
       coalesce(sum(cost_micros), 0)::bigint,
       coalesce(sum(input_tokens), 0)::bigint,
       coalesce(sum(output_tokens), 0)::bigint,
       (count(*) FILTER (WHERE noisy_fixed))::int,
       (count(*) FILTER (WHERE recovered))::int,
       coalesce(sum(downtime_ms), 0)::bigint,
       rollup_hist((array_agg(rollup_hist_bucket(downtime_ms)) FILTER (WHERE downtime_ms IS NOT NULL))::int[]),
       coalesce(sum(timed_out), 0)::int
FROM per
GROUP BY hour, route, service, model, verdict;

-- name: RecountQualityDaily :exec
-- RecountQualityDaily counts, for each UTC day that starts at one of
-- buckets, the investigations created on it that people rated, by the
-- verdict Sidekick gave and the one people confirmed, into quality_daily.
-- The rows of the days must be deleted first. An investigation counts when
-- it has a confirmed verdict, or when someone rated its current verdict
-- wrong without giving the real one, which counts as confirmed 'unknown'.
-- The range on created_at uses the index on it, and feedback_investigation
-- finds the ratings.
INSERT INTO quality_daily (day, route, service, model, strategy, predicted, confirmed, n)
SELECT (b.day AT TIME ZONE 'UTC')::date,
       i.route,
       coalesce(i.service, ''),
       coalesce(i.model, ''),
       coalesce(i.strategy, ''),
       i.verdict,
       coalesce(i.confirmed_verdict, 'unknown'),
       count(*)::int
FROM unnest(@buckets::timestamptz[]) AS b(day)
JOIN investigations i ON i.created_at >= b.day AND i.created_at < b.day + interval '24 hours'
LEFT JOIN LATERAL (
  SELECT true AS wrong
  FROM feedback f
  WHERE f.investigation_id = i.id
    AND f.target_kind = 'verdict'
    AND f.target_ref = 'run:' || i.run::text || '/verdict'
    AND f.rating = 'wrong'
  LIMIT 1
) rated ON true
WHERE i.verdict IS NOT NULL
  AND (i.confirmed_verdict IS NOT NULL OR rated.wrong)
GROUP BY 1, 2, 3, 4, 5, 6, 7;
