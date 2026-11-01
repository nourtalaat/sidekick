-- 100 runtime strategies with 200 versions each, for the query plan tests.
INSERT INTO strategies (name, current_version, created_at)
SELECT 'strategy-' || s, 200, now() FROM generate_series(1, 100) AS s;

INSERT INTO strategy_versions (name, version, body, reason, created_at)
SELECT 'strategy-' || s, v, 'name: strategy-' || s || E'\ndescription: ' || repeat('x', 2000) || E'\n', 'Reason ' || v, now()
FROM generate_series(1, 100) AS s, generate_series(1, 200) AS v;

ANALYZE strategies;
ANALYZE strategy_versions;
