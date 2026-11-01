-- Strategy proposals for the proposal API tests, with fixture/strategies_api.sql
-- (checkout-latency at runtime:2, the strategy file file-latency).
--
--   0301  passed, for checkout-latency at runtime:2, with a gate result and
--         two wrong ratings it was drafted from
--   0302  failed, its gate found the proposal worse
--   0303  pending, waiting for its gate
--   0304  drafting, hidden from the list unless asked for
--   0305  draft_failed, hidden too
--   0306  passed, for the strategy file file-latency
--   0307  passed, but drafted from checkout-latency at runtime:1, so stale
--
-- They are created 10 minutes apart, 0301 the newest.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
VALUES ('01920000-0000-7000-8000-000000000201', 'fp-1', 'payments', 'checkout-api', 'closed', 1,
        '2026-10-26 10:00:00+00', '2026-10-26 10:00:00+00', '01920000-0000-7000-8000-000000000101');

INSERT INTO investigations (id, group_id, route, strategy, strategy_version, mode, status, run, verdict, confidence, report,
                            stop_reason, connectors, budget, created_at, started_at, finished_at)
VALUES ('01920000-0000-7000-8000-000000000101', '01920000-0000-7000-8000-000000000201', 'payments', 'checkout-latency', 'runtime:2',
        'read-only', 'final', 1, 'true_positive', 'high', '{"verdict": "true_positive", "summary": "A summary"}',
        'submitted', '{}', '{}', '2026-10-26 10:00:00+00', '2026-10-26 10:00:01+00', '2026-10-26 10:02:00+00');

INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, reason, correct_verdict, slack_user_id, channel, created_at, updated_at) VALUES
  ('01920000-0000-7000-8000-000000000401', '01920000-0000-7000-8000-000000000101', 'verdict', 'run:1/verdict', 'wrong',
   'It was a planned deploy.', 'false_positive', 'U01', 'slack', '2026-10-26 10:05:00+00', '2026-10-26 10:05:00+00'),
  ('01920000-0000-7000-8000-000000000402', '01920000-0000-7000-8000-000000000101', 'finding', 'run:1/finding:f1', 'wrong',
   NULL, NULL, 'U02', 'slack', '2026-10-26 10:06:00+00', '2026-10-26 10:06:00+00');

INSERT INTO bench_runs (id, started_at, finished_at, sidekick_version, git_sha, config_hash, connectors, strategies,
                        scenario_set, repeats, summary, status, triggered_by) VALUES
  ('01920000-0000-7000-8000-000000000501', now(), now(), 'dev', 'abc', 'hash', '{}', '{}', 'gate:checkout-latency', 5, '{}', 'passed', 'gate'),
  ('01920000-0000-7000-8000-000000000502', now(), now(), 'dev', 'abc', 'hash', '{}', '{}', 'gate:checkout-latency', 5, '{}', 'passed', 'gate'),
  ('01920000-0000-7000-8000-000000000503', now(), now(), 'dev', 'abc', 'hash', '{}', '{}', 'gate:file-latency', 5, '{}', 'passed', 'gate'),
  ('01920000-0000-7000-8000-000000000504', now(), now(), 'dev', 'abc', 'hash', '{}', '{}', 'gate:file-latency', 5, '{}', 'passed', 'gate'),
  ('01920000-0000-7000-8000-000000000505', now(), now(), 'dev', 'abc', 'hash', '{}', '{}', 'gate:checkout-latency', 5, '{}', 'passed', 'gate'),
  ('01920000-0000-7000-8000-000000000506', now(), now(), 'dev', 'abc', 'hash', '{}', '{}', 'gate:checkout-latency', 5, '{}', 'passed', 'gate');

INSERT INTO strategy_proposals (id, name, base_version, base_origin, base_body, body, rationale, feedback_ids, trigger_count, status,
                                status_detail, gate_result, gate_detail, bench_run_id, baseline_run_id, created_at, updated_at) VALUES
  ('01920000-0000-7000-8000-000000000301', 'checkout-latency', 'runtime:2', 'runtime',
   E'name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check what changed.\n',
   E'name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check what changed.\n  - Ask whether a deploy was planned.\n',
   'Planned deploys were reported as incidents.',
   '{01920000-0000-7000-8000-000000000401,01920000-0000-7000-8000-000000000402}', 3, 'passed', NULL, 'passed',
   '{"result": "passed", "reason": "The proposal is not clearly worse than the current version.", "mean": 0.01,
     "interval": {"lower": -0.005, "upper": 0.03}, "maxDrop": 0.02, "resamples": 10000, "seed": 42, "repeats": 5,
     "costMicros": 1200000, "maxCostMicros": 5000000,
     "scenarios": [{"id": "s1", "baseline": 0.8, "proposal": 0.82, "difference": 0.02}, {"id": "s2", "baseline": 0.5, "proposal": 0.5, "difference": 0}]}',
   '01920000-0000-7000-8000-000000000501', '01920000-0000-7000-8000-000000000502', now() - interval '10 minutes', now() - interval '10 minutes'),
  ('01920000-0000-7000-8000-000000000302', 'checkout-latency', 'runtime:3', 'runtime', 'name: checkout-latency', E'name: checkout-latency\ndescription: Worse.\n',
   'Dropped the checks.', '{}', 3, 'failed', NULL, 'failed',
   '{"result": "failed", "reason": "The proposal scores lower.", "mean": -0.3, "interval": {"lower": -0.4, "upper": -0.2}, "maxDrop": 0.02,
     "resamples": 10000, "seed": 7, "repeats": 5, "costMicros": 900000, "maxCostMicros": 5000000, "scenarios": []}',
   NULL, NULL, now() - interval '20 minutes', now() - interval '20 minutes'),
  ('01920000-0000-7000-8000-000000000303', 'checkout-latency', 'runtime:4', 'runtime', 'name: checkout-latency', E'name: checkout-latency\ndescription: Pending.\n',
   'Waiting.', '{}', 3, 'pending', NULL, NULL, NULL, NULL, NULL, now() - interval '30 minutes', now() - interval '30 minutes'),
  ('01920000-0000-7000-8000-000000000304', 'checkout-latency', 'runtime:5', NULL, NULL, NULL, NULL, '{}', 3, 'drafting', NULL, NULL, NULL, NULL, NULL,
   now() - interval '40 minutes', now() - interval '40 minutes'),
  ('01920000-0000-7000-8000-000000000305', 'checkout-latency', 'runtime:6', NULL, NULL, NULL, NULL, '{}', 3, 'draft_failed', 'The model did not return a valid strategy.',
   NULL, NULL, NULL, NULL, now() - interval '50 minutes', now() - interval '50 minutes'),
  ('01920000-0000-7000-8000-000000000306', 'file-latency', '0123456789abcdef', 'file', E'name: file-latency\ndescription: A strategy from a file.\n',
   E'name: file-latency\ndescription: A strategy from a file, changed.\n', 'Changed it.', '{}', 3, 'passed', NULL, 'passed',
   '{"result": "passed", "reason": "Fine.", "mean": 0, "interval": {"lower": 0, "upper": 0}, "maxDrop": 0.02, "resamples": 10000, "seed": 9, "repeats": 5,
     "costMicros": 100, "maxCostMicros": 5000000, "scenarios": []}',
   '01920000-0000-7000-8000-000000000503', '01920000-0000-7000-8000-000000000504', now() - interval '60 minutes', now() - interval '60 minutes'),
  ('01920000-0000-7000-8000-000000000307', 'checkout-latency', 'runtime:1', 'runtime', E'name: checkout-latency\ndescription: Checkout is slow.\n',
   E'name: checkout-latency\ndescription: Checkout is slow, from the first version.\n', 'Old.', '{}', 3, 'passed', NULL, 'passed',
   '{"result": "passed", "reason": "Fine.", "mean": 0, "interval": {"lower": 0, "upper": 0}, "maxDrop": 0.02, "resamples": 10000, "seed": 9, "repeats": 5,
     "costMicros": 100, "maxCostMicros": 5000000, "scenarios": []}',
   '01920000-0000-7000-8000-000000000505', '01920000-0000-7000-8000-000000000506', now() - interval '70 minutes', now() - interval '70 minutes');
