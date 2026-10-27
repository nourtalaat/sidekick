-- A busy database for the rate limit query plan tests: 50,000 events and
-- a full window of 600 admissions for each of 100 scopes (60,000 rows).
INSERT INTO events (id, intake, key, kind, trusted, title, labels, fingerprint, status, received_at)
SELECT md5('event' || i)::uuid, 'alertmanager', 'key-' || i, 'alert', true, 'Alert ' || i, '{}', 'fp-' || i,
       CASE WHEN i % 100 = 0 THEN 'received' ELSE 'routed' END,
       '2026-10-26 14:00:00+00'::timestamptz - make_interval(secs => i)
FROM generate_series(1, 50000) AS i;

INSERT INTO rate_admissions (scope, event_id, admitted_at)
SELECT 'route:route-' || s, md5('admitted' || s || '-' || n)::uuid,
       '2026-10-26 14:00:00+00'::timestamptz - make_interval(secs => n * 6)
FROM generate_series(1, 100) AS s, generate_series(1, 600) AS n;

ANALYZE;
