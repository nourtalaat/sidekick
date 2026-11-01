-- 20,000 lessons for the query plan tests: 5,000 active ones with a scope
-- (3,000 for a service, 100 more for a service and an alert, 1,500 for an
-- alert, 400 for a route and 100 for a strategy), 40 active global ones,
-- and 14,960 that are not active, each drafted from a feedback row with the
-- ID md5('fb' || i). Lesson i has the ID md5('lesson' || i). Also 5,000
-- investigations, md5('inv' || i), each with a rating, and 50,000 steps on
-- the first 1,000 of them.
INSERT INTO lessons (id, text, scope_service, scope_alert, scope_route, scope_strategy, status, origin, helpful, unhelpful, created_at)
SELECT md5('lesson' || i)::uuid,
       'Lesson ' || i || ' says to check the deploys, the connection pools and the queues first.',
       CASE WHEN i <= 3000 THEN 'svc-' || (i % 500) END,
       CASE WHEN i <= 100 OR (i > 3000 AND i <= 4500) THEN 'alert-' || (i % 800) END,
       CASE WHEN i > 4500 AND i <= 4900 THEN 'route-' || (i % 20) END,
       CASE WHEN i > 4900 AND i <= 5000 THEN 'strategy-' || (i % 5) END,
       'active', 'ui', i % 7, i % 3,
       '2026-10-01 00:00:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(1, 5000) AS i;

INSERT INTO lessons (id, text, status, origin, helpful, unhelpful, created_at)
SELECT md5('lesson' || i)::uuid, 'Global lesson ' || i || ' says to state what you could not check.', 'active', 'ui', i % 5, i % 2,
       '2026-10-01 00:00:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(5001, 5040) AS i;

INSERT INTO lessons (id, text, scope_service, status, origin, feedback_ids, created_at)
SELECT md5('lesson' || i)::uuid, 'Lesson ' || i || ' is not in use.', 'svc-' || (i % 500),
       CASE i % 3 WHEN 0 THEN 'proposed' WHEN 1 THEN 'disabled' ELSE 'rejected' END,
       CASE WHEN i % 3 = 0 THEN 'feedback' ELSE 'ui' END,
       ARRAY[md5('fb' || i)::uuid],
       '2026-10-01 00:00:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(5041, 20000) AS i;

INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
SELECT md5('grp' || i)::uuid, 'fp-' || i, 'payments', 'checkout-api', 'closed', 1,
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       md5('inv' || i)::uuid
FROM generate_series(1, 5000) AS i;

INSERT INTO investigations (id, group_id, route, strategy, mode, status, run, verdict, confidence, report, stop_reason,
                            connectors, budget, created_at, started_at, finished_at)
SELECT md5('inv' || i)::uuid, md5('grp' || i)::uuid, 'payments', 'generic-alert', 'read-only', 'final', 1,
       'true_positive', 'high', '{"verdict": "true_positive", "cause": "Deploy 4f2c1a"}', 'submitted', '{}', '{}',
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:00:01+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:02:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(1, 5000) AS i;

INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, reason, user_id, channel, created_at, updated_at)
SELECT md5('fb' || i)::uuid, md5('inv' || i)::uuid, 'verdict', 'run:1/verdict', 'wrong', 'The deploy was the cause.',
       md5('user' || i)::uuid, 'ui', '2026-10-26 00:03:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:03:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(1, 5000) AS i;

INSERT INTO steps (investigation_id, seq, run, kind, parts, source, tool, tool_call_id, status, started_at, duration_ms)
SELECT md5('inv' || i)::uuid, q, 1, 'tool_result', '[]', 'signoz', 'search_logs', 'call-' || i || '-' || q, 'ok',
       '2026-10-26 00:00:10+00'::timestamptz + i * interval '1 minute', 100
FROM generate_series(1, 1000) AS i, generate_series(0, 49) AS q;

ANALYZE lessons, event_groups, investigations, feedback, steps;
