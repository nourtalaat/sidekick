-- For the query plan tests: 20,000 final investigations of 20 strategies with
-- 10 versions each, 30 per minute-step over 60 days, each with a groups row.
-- Every investigation has a rating of its verdict, wrong for a third of them,
-- and every tenth has a confirmed verdict. 2,000 proposals of 100 strategies
-- in all statuses, and a gate with 130 finished runs.
--
-- Investigation i has the ID md5('inv' || i), runs strategy 'strategy-' ||
-- (i % 20) at version 'v' || (i % 10), and was rated at now() minus i
-- minutes times 4, so about 3,600 ratings are from the last 14 days.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
SELECT md5('grp' || i)::uuid, 'fp-' || i, 'payments', 'checkout-api', 'closed', 1,
       now() - i * interval '4 minutes', now() - i * interval '4 minutes', md5('inv' || i)::uuid
FROM generate_series(1, 20000) AS i;

INSERT INTO investigations (id, group_id, route, strategy, strategy_version, mode, status, run, verdict, confidence, report,
                            stop_reason, connectors, budget, created_at, started_at, finished_at,
                            confirmed_verdict, confirmed_cause, confirmed_at)
SELECT md5('inv' || i)::uuid, md5('grp' || i)::uuid, 'payments', 'strategy-' || (i % 20), 'v' || (i % 10),
       'read-only', 'final', 1, 'true_positive', 'high', '{"verdict": "true_positive", "summary": "A summary"}',
       'submitted', '{}', '{}', now() - i * interval '4 minutes', now() - i * interval '4 minutes',
       now() - i * interval '4 minutes',
       CASE WHEN i % 10 = 0 THEN 'true_positive' END, CASE WHEN i % 10 = 0 THEN 'A cause' END,
       CASE WHEN i % 10 = 0 THEN now() - i * interval '4 minutes' END
FROM generate_series(1, 20000) AS i;

INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, reason, slack_user_id, channel, created_at, updated_at)
SELECT md5('fb' || i)::uuid, md5('inv' || i)::uuid, 'verdict', 'run:1/verdict',
       CASE WHEN i % 3 = 0 THEN 'wrong' ELSE 'correct' END, 'The verdict is not right', 'U01', 'slack',
       now() - i * interval '4 minutes', now() - i * interval '4 minutes'
FROM generate_series(1, 20000) AS i;

INSERT INTO bench_runs (id, started_at, finished_at, sidekick_version, git_sha, config_hash, connectors, strategies,
                        scenario_set, repeats, summary, status, triggered_by)
SELECT md5('run' || r)::uuid, now(), now(), 'dev', 'abc', 'hash', '{}', '{}', 'gate', 5, '{}', 'passed', 'gate'
FROM generate_series(1, 4) AS r;

INSERT INTO strategy_proposals (id, name, base_version, base_origin, base_body, body, rationale, feedback_ids, trigger_count,
                                status, gate_result, bench_run_id, baseline_run_id, created_at, updated_at)
SELECT md5('prop' || p)::uuid, 'strategy-' || (p % 100), 'v' || p, 'file', 'name: x', 'name: y', 'Because', '{}', 3,
       CASE WHEN p % 4 = 0 THEN 'passed' WHEN p % 4 = 1 THEN 'failed' WHEN p % 4 = 2 THEN 'rejected' ELSE 'pending' END,
       CASE WHEN p % 4 = 0 THEN 'passed' WHEN p % 4 = 1 THEN 'failed' END,
       CASE WHEN p % 4 = 0 THEN md5('run1')::uuid END, CASE WHEN p % 4 = 0 THEN md5('run2')::uuid END,
       now() - p * interval '10 minutes', now() - p * interval '10 minutes'
FROM generate_series(1, 2000) AS p;

INSERT INTO strategy_gate_runs (proposal_id, arm, scenario_id, repeat, result, cost_micros, created_at)
SELECT md5('prop' || 1)::uuid, a, 'scenario-' || s, r, '{}', 1000, now()
FROM unnest(ARRAY['baseline', 'proposal']) AS a, generate_series(1, 13) AS s, generate_series(1, 5) AS r;

ANALYZE event_groups, investigations, feedback, strategy_proposals, strategy_gate_runs;
