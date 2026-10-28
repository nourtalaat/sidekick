-- A busy database for the query plan tests: 50,000 events from two Teams
-- listeners, mentions and a webhook endpoint.
INSERT INTO events (id, intake, key, kind, trusted, title, labels, fingerprint, status, received_at)
SELECT md5('event' || i)::uuid,
       (ARRAY['alerts', 'ops', 'teams-mention', 'alertmanager'])[1 + i % 4],
       '19:alerts@thread.tacv2/' || (1761642000000 + i), 'alert', false, 'Alert ' || i, '{}', 'fp-' || i, 'routed',
       '2026-10-28 09:00:00+00'::timestamptz - make_interval(secs => i)
FROM generate_series(1, 50000) AS i;

ANALYZE;
