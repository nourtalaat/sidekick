-- 20,000 final investigations, each with its group, five ratings of its
-- verdict by five Slack users (100,000 ratings) and two by signed-in users
-- (40,000 more), and 50,000 steps on 1,000 of them (the ones numbered 9,501
-- to 10,500), for the query plan tests. Investigation i has the ID
-- md5('inv' || i), and signed-in user u has the ID md5('user' || u).
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
SELECT md5('grp' || i)::uuid, 'fp-' || i, 'payments', 'checkout-api', 'closed', 1,
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       md5('inv' || i)::uuid
FROM generate_series(1, 20000) AS i;

INSERT INTO investigations (id, group_id, route, strategy, mode, status, run, verdict, confidence, report, stop_reason,
                            connectors, budget, created_at, started_at, finished_at)
SELECT md5('inv' || i)::uuid, md5('grp' || i)::uuid, 'payments', 'generic-alert', 'read-only', 'final', 1,
       'true_positive', 'high', '{"verdict": "true_positive", "cause": "Deploy 4f2c1a"}', 'submitted', '{}', '{}',
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:00:01+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:02:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(1, 20000) AS i;

INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, slack_user_id, channel, created_at, updated_at)
SELECT md5('fb' || i || '-' || u)::uuid, md5('inv' || i)::uuid, 'verdict', 'run:1/verdict',
       CASE WHEN u % 3 = 0 THEN 'wrong' ELSE 'correct' END, 'U0' || u, 'slack',
       '2026-10-26 00:03:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:03:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(1, 20000) AS i, generate_series(1, 5) AS u;

INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, user_id, channel, created_at, updated_at)
SELECT md5('ufb' || i || '-' || u)::uuid, md5('inv' || i)::uuid, 'verdict', 'run:1/verdict',
       CASE WHEN u = 1 THEN 'wrong' ELSE 'correct' END, md5('user' || u)::uuid, 'ui',
       '2026-10-26 00:04:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:04:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(1, 20000) AS i, generate_series(1, 2) AS u;

INSERT INTO steps (investigation_id, seq, run, kind, parts, source, tool, tool_call_id, status, started_at, duration_ms)
SELECT md5('inv' || i)::uuid, q, 1, 'tool_result', '[]', 'signoz', 'search_logs', 'call-' || i || '-' || q, 'ok',
       '2026-10-26 00:00:10+00'::timestamptz + i * interval '1 minute', 100
FROM generate_series(9501, 10500) AS i, generate_series(0, 49) AS q;

ANALYZE event_groups, investigations, feedback, steps;
