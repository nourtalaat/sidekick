-- Rollup rows with known answers for the tests of the stats endpoints, for
-- a server whose clock is 2026-10-28 12:00 UTC. The default range of a
-- request is then 2026-09-29 to 2026-10-28, and the range before it
-- 2026-08-30 to 2026-09-28. The rows are written as the rollup job writes
-- them, so the tests of the endpoints do not depend on the job.

-- Quality: 17 rated in the default range, 13 of them correct; 4 rated in
-- the range before, 3 of them correct.
INSERT INTO quality_daily (day, route, service, model, strategy, predicted, confirmed, n)
VALUES
  ('2026-10-20', 'r1', 'checkout', 'm1', 'default', 'false_positive', 'false_positive', 8),
  ('2026-10-20', 'r1', 'checkout', 'm1', 'default', 'false_positive', 'true_positive', 2),
  ('2026-10-20', 'r1', 'checkout', 'm1', 'default', 'true_positive', 'true_positive', 3),
  ('2026-10-21', 'r1', 'checkout', 'm1', 'default', 'false_positive', 'unknown', 1),
  ('2026-10-21', 'r2', 'payments', 'm2', 'deep', 'true_positive', 'true_positive', 2),
  ('2026-10-21', 'r2', 'payments', 'm2', 'deep', 'true_positive', 'false_positive', 1),
  ('2026-09-10', 'r1', 'checkout', 'm1', 'default', 'false_positive', 'false_positive', 3),
  ('2026-09-10', 'r1', 'checkout', 'm1', 'default', 'true_positive', 'false_positive', 1);

-- Spend: the day, route, connector and model of each model call's cost.
INSERT INTO spend_daily (day, route, connector, model, cost_micros, input_tokens, output_tokens)
VALUES
  ('2026-10-20', 'r1', 'claude', 'm1', 20000, 10000, 5000),
  ('2026-10-20', 'r2', 'claude', 'm2', 5000, 2000, 1000),
  ('2026-10-21', 'r1', 'claude', 'm1', 10000, 4000, 2000),
  ('2026-10-21', 'r2', 'openai', 'm2', 6000, 3000, 1500),
  ('2026-10-28', 'r1', 'claude', 'm1', 40000, 16000, 8000),
  ('2026-10-28', 'r2', 'claude', 'm2', 25000, 9000, 4500),
  ('2026-09-10', 'r1', 'claude', 'm1', 10000, 5000, 2500);

-- Stats. Both histograms are 24 counts; bucket 9 is about 30 seconds, 11
-- about a minute and 16 about 12 to 20 minutes.
INSERT INTO stats_hourly (
  hour, route, service, model, verdict,
  investigations, finished, duration_n, duration_ms_sum, first_finding_n, first_finding_ms_sum, first_finding_hist,
  tool_calls, cost_micros, input_tokens, output_tokens, noisy_fixed,
  actions_proposed, actions_approved, actions_executed,
  recoveries, downtime_ms_sum, downtime_hist, watch_timeouts
)
VALUES
  ('2026-10-20 10:00:00+00', 'r1', 'checkout', 'm1', 'false_positive',
   10, 10, 10, 1200000, 8, 400000, ARRAY[0,0,0,0,0,0,0,0,0,0,8,0,0,0,0,0,0,0,0,0,0,0,0,0],
   50, 20000, 10000, 5000, 2, 0, 0, 0, 0, 0, ARRAY[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0], 1),
  ('2026-10-20 10:00:00+00', 'r1', 'checkout', 'm1', 'true_positive',
   4, 4, 4, 1200000, 4, 120000, ARRAY[0,0,0,0,0,0,0,0,0,4,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
   40, 20000, 8000, 4000, 0, 0, 0, 0, 2, 1440000, ARRAY[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,2,0,0,0,0,0,0,0], 0),
  ('2026-10-21 09:00:00+00', 'r2', 'payments', 'm2', 'false_positive',
   6, 6, 6, 360000, 6, 360000, ARRAY[0,0,0,0,0,0,0,0,0,0,0,6,0,0,0,0,0,0,0,0,0,0,0,0],
   12, 6000, 3000, 1500, 1, 0, 0, 0, 1, 600000, ARRAY[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,1,0,0,0,0,0,0,0], 0),
  ('2026-10-21 09:00:00+00', 'r2', 'payments', 'm2', 'inconclusive',
   2, 2, 2, 100000, 0, 0, ARRAY[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
   2, 1000, 500, 250, 0, 0, 0, 0, 0, 0, ARRAY[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0], 0),
  ('2026-10-22 11:00:00+00', 'r1', '', '', '',
   1, 0, 0, 0, 0, 0, ARRAY[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
   1, 300, 100, 50, 0, 0, 0, 0, 0, 0, ARRAY[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0], 0),
  ('2026-09-10 12:00:00+00', 'r1', 'checkout', 'm1', 'false_positive',
   5, 5, 5, 500000, 5, 150000, ARRAY[0,0,0,0,0,0,0,0,0,5,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
   20, 10000, 5000, 2500, 0, 0, 0, 0, 0, 0, ARRAY[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0], 0),
  ('2024-01-01 10:00:00+00', 'old-route', 'old-service', 'old-model', 'true_positive',
   1, 1, 1, 1000, 1, 1000, ARRAY[0,1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],
   1, 1, 1, 1, 0, 0, 0, 0, 0, 0, ARRAY[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0], 0);
