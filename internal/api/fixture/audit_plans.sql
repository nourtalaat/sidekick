-- 100,000 audit entries, one a minute for the last 69 days, for the plan
-- tests of GET /audit. A twentieth of them are made by policy, a twentieth
-- by Sidekick, and the rest by 50 users; 12 actions share the rows; a
-- fifth belong to one of 2,000 investigations. 30 entries more have an
-- actor that makes nothing else, 30 an action nothing else has, and 30 both.
INSERT INTO audit_log (at, actor_kind, actor_id, action, target_kind, target_id, investigation_id, details)
SELECT now() - make_interval(mins => n),
       CASE n % 20 WHEN 0 THEN 'policy' WHEN 1 THEN 'sidekick' ELSE 'user' END,
       CASE WHEN n % 20 IN (0, 1) THEN NULL ELSE 'user-' || (n % 50) END,
       'action.' || (n % 12),
       'route',
       'target-' || (n % 100),
       CASE WHEN n % 5 = 0 THEN md5('investigation' || (n % 2000))::uuid END,
       '{"new": {"priority": 1}}'
FROM generate_series(1, 100000) AS n;

INSERT INTO audit_log (at, actor_kind, actor_id, action, target_kind, target_id, details)
SELECT now() - make_interval(mins => n * 3000), 'user', 'user-rare', 'action.' || (n % 12), 'route', 'target-1', '{}'
FROM generate_series(1, 30) AS n;

INSERT INTO audit_log (at, actor_kind, actor_id, action, target_kind, target_id, details)
SELECT now() - make_interval(mins => n * 3000 + 1), 'user', 'user-' || (n % 50), 'action.rare', 'route', 'target-1', '{}'
FROM generate_series(1, 30) AS n;

INSERT INTO audit_log (at, actor_kind, actor_id, action, target_kind, target_id, details)
SELECT now() - make_interval(mins => n * 3000 + 2), 'user', 'user-rare', 'action.rare', 'route', 'target-1', '{}'
FROM generate_series(1, 30) AS n;

ANALYZE audit_log;
