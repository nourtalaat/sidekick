-- Two years of rollups for the query plan tests of the dashboard metrics,
-- ending on the day the test runs: 17,520 hours of stats in 3 routes, 20
-- services, 3 models and 6 verdicts (2 rows an hour), quality rows for
-- every day (up to 40 a day), and spend for every day, route, connector
-- and model.
INSERT INTO stats_hourly (
  hour, route, service, model, verdict,
  investigations, finished, duration_n, duration_ms_sum, first_finding_n, first_finding_ms_sum, first_finding_hist,
  tool_calls, cost_micros, input_tokens, output_tokens, noisy_fixed,
  recoveries, downtime_ms_sum, downtime_hist, watch_timeouts
)
SELECT date_trunc('hour', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - make_interval(hours => h),
       'route-' || ((h + r) % 3), 'service-' || ((h * 7 + r) % 20), 'model-' || ((h + r) % 3),
       (ARRAY['true_positive', 'false_positive', 'inconclusive', 'duplicate', 'known_issue', 'flapping'])[1 + (h + r) % 6],
       3, 3, 3, 300000, 3, 90000, array_fill(0, ARRAY[24]),
       12, 3000, 1500, 600, (h % 5 = 0)::int,
       (h % 7 = 0)::int, CASE WHEN h % 7 = 0 THEN 600000 ELSE 0 END, array_fill(0, ARRAY[24]), (h % 11 = 0)::int
FROM generate_series(0, 17519) AS h, generate_series(0, 1) AS r
ON CONFLICT DO NOTHING;

INSERT INTO quality_daily (day, route, service, model, strategy, predicted, confirmed, n)
SELECT (now() AT TIME ZONE 'UTC')::date - d,
       'route-' || (i % 3), 'service-' || (i % 20), 'model-' || (i % 3), 'strategy-' || (i % 2),
       (ARRAY['true_positive', 'false_positive', 'inconclusive', 'duplicate', 'known_issue', 'flapping'])[1 + i % 6],
       (ARRAY['true_positive', 'false_positive', 'inconclusive', 'duplicate', 'known_issue', 'flapping', 'unknown'])[1 + (i / 2) % 7],
       1 + i % 4
FROM generate_series(0, 729) AS d, generate_series(1, 40) AS i
ON CONFLICT DO NOTHING;

INSERT INTO spend_daily (day, route, connector, model, cost_micros, input_tokens, output_tokens)
SELECT (now() AT TIME ZONE 'UTC')::date - d, 'route-' || r, 'connector-' || c, 'model-' || m, 1000 + d, 500, 250
FROM generate_series(0, 729) AS d, generate_series(0, 2) AS r, generate_series(0, 1) AS c, generate_series(0, 2) AS m
ON CONFLICT DO NOTHING;

ANALYZE stats_hourly, quality_daily, spend_daily;
