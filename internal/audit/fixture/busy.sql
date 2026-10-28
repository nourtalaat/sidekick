-- A busy audit log for the query plan tests: 50,000 entries, one in ten
-- of them about an investigation.
INSERT INTO audit_log (at, actor_kind, actor_id, action, target_kind, target_id, investigation_id, details)
SELECT now() - make_interval(secs => i), 'user', 'U' || (i % 50), 'route.updated', 'route', 'route-' || (i % 200),
       CASE WHEN i % 10 = 0 THEN md5('inv' || i)::uuid END,
       '{"old": {"priority": 100}, "new": {"priority": 50}}'
FROM generate_series(1, 50000) AS i;
