-- 20,000 lessons for the query plan tests: 5,000 active ones with a scope
-- (3,000 for a service, 100 more for a service and an alert, 1,500 for an
-- alert, 400 for a route and 100 for a strategy), 40 active global ones,
-- and 14,960 that are not active. Lesson i has the ID md5('lesson' || i).
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

INSERT INTO lessons (id, text, scope_service, status, origin, created_at)
SELECT md5('lesson' || i)::uuid, 'Lesson ' || i || ' is not in use.', 'svc-' || (i % 500),
       CASE i % 3 WHEN 0 THEN 'proposed' WHEN 1 THEN 'disabled' ELSE 'rejected' END,
       CASE WHEN i % 3 = 0 THEN 'feedback' ELSE 'ui' END,
       '2026-10-01 00:00:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(5041, 20000) AS i;

ANALYZE lessons;
