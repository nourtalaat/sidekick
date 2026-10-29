-- A year of investigations for the query plan tests: 20,000 investigations
-- in 20,000 groups, one event each, a watcher for every 10th, a rating for
-- every 5th, their rollup rows, in 3 routes, 20 services, 3 models and 6 verdicts, and one
-- dirty mark for every hour and day of the year.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, closed_reason, investigation_id)
SELECT md5('group' || i)::uuid, 'fp-' || i, 'route-' || (i % 3), 'closed', 1,
       now() - make_interval(secs => i * 1577), now() - make_interval(secs => i * 1577), 'window_ended',
       md5('investigation' || i)::uuid
FROM generate_series(1, 20000) AS i;

INSERT INTO investigations (
  id, group_id, route, strategy, mode, status, verdict, connectors, budget, service, model,
  tool_calls, cost_micros, input_tokens, output_tokens,
  created_at, started_at, first_finding_at, finished_at, confirmed_verdict
)
SELECT md5('investigation' || i)::uuid, md5('group' || i)::uuid, 'route-' || (i % 3), 'strategy-' || (i % 2), 'read-only', 'final',
       (ARRAY['true_positive', 'false_positive', 'inconclusive', 'duplicate', 'known_issue', 'flapping'])[1 + i % 6],
       '{}', '{}', 'service-' || (i % 20), 'model-' || (i % 3),
       i % 12, 1000 + i % 5000, 1000 + i % 700, 200 + i % 300,
       now() - make_interval(secs => i * 1577),
       now() - make_interval(secs => i * 1577 - 5),
       now() - make_interval(secs => i * 1577 - 60),
       now() - make_interval(secs => i * 1577 - 300),
       CASE WHEN i % 5 = 0 THEN (ARRAY['true_positive', 'false_positive'])[1 + i % 2] END
FROM generate_series(1, 20000) AS i;

INSERT INTO events (id, intake, key, kind, trusted, fingerprint, status, group_id, starts_at, received_at)
SELECT md5('event' || i)::uuid, 'alertmanager', 'key-' || i, 'alert', true, 'fp-' || i, 'grouped', md5('group' || i)::uuid,
       now() - make_interval(secs => i * 1577 + 30), now() - make_interval(secs => i * 1577)
FROM generate_series(1, 20000) AS i;

INSERT INTO watchers (id, group_id, investigation_id, name, origin, check_type, spec, interval_s, passes_to_recover,
                      check_timeout_s, status, started_at, deadline, recovered_at)
SELECT md5('watcher' || i)::uuid, md5('group' || i)::uuid, md5('investigation' || i)::uuid, 'health', 'model', 'alert',
       '{}', 30, 2, 10, 'recovered', now() - make_interval(secs => i * 1577 - 10), now() - make_interval(secs => i * 1577 - 3600),
       now() - make_interval(secs => i * 1577 - 900)
FROM generate_series(10, 20000, 10) AS i;

INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, slack_user_id, channel, created_at, updated_at)
SELECT md5('feedback' || i)::uuid, md5('investigation' || i)::uuid, 'verdict', 'run:1/verdict',
       (ARRAY['correct', 'wrong'])[1 + i % 2], 'U1', 'slack', now() - make_interval(secs => i * 1577 - 400), now() - make_interval(secs => i * 1577 - 400)
FROM generate_series(5, 20000, 5) AS i;

DELETE FROM rollup_dirty;

INSERT INTO rollup_dirty (table_name, bucket)
SELECT 'stats_hourly', date_trunc('hour', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - make_interval(hours => h)
FROM generate_series(0, 8759) AS h;

INSERT INTO rollup_dirty (table_name, bucket)
SELECT 'quality_daily', date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - make_interval(days => d)
FROM generate_series(0, 364) AS d;

-- The rows the recounts replace.
INSERT INTO stats_hourly (hour, route, service, model, verdict, investigations, finished, first_finding_hist, downtime_hist)
SELECT date_trunc('hour', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', route, coalesce(service, ''), coalesce(model, ''), verdict,
       1, 1, array_fill(0, ARRAY[24]), array_fill(0, ARRAY[24])
FROM investigations
ON CONFLICT DO NOTHING;

INSERT INTO quality_daily (day, route, service, model, strategy, predicted, confirmed, n)
SELECT (created_at AT TIME ZONE 'UTC')::date, route, service, model, strategy, verdict, confirmed_verdict, 1
FROM investigations
WHERE confirmed_verdict IS NOT NULL
ON CONFLICT DO NOTHING;

ANALYZE;
