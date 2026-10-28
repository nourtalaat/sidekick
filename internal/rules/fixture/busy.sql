-- Many runtime rules for the query plan tests: 5,000 listeners and 5,000
-- routes, far more than a real team has.
INSERT INTO listeners (name, slack_channel, match, trusted, enabled, version, updated_by, updated_at)
SELECT 'listener-' || i, '#channel-' || i, '{"sender": {"kind": "bot"}}', false, true, 1, 'U01SEED', now()
FROM generate_series(1, 5000) AS i;

INSERT INTO routes (name, priority, spec, enabled, version, updated_by, updated_at)
SELECT 'route-' || i, 100, '{"match": {"service": "svc-*"}, "mode": "read-only"}', true, 1, 'U01SEED', now()
FROM generate_series(1, 5000) AS i;

ANALYZE listeners;
ANALYZE routes;
