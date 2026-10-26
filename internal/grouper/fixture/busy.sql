-- A busy database for the query plan tests: 50,000 groups, one in ten
-- open, each with two events, a fingerprint of its own for the open ones
-- and ten groups per fingerprint for the closed ones.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, window_until, closed_reason)
SELECT md5('group' || i)::uuid,
       CASE WHEN i % 10 = 0 THEN 'fp-open-' || i ELSE 'fp-' || (i % 5000) END,
       'default',
       CASE WHEN i % 10 = 0 THEN 'open' ELSE 'closed' END,
       2,
       now() - make_interval(secs => i * 60),
       now() - make_interval(secs => i * 60),
       now() - make_interval(secs => i * 60) + interval '30 minutes',
       CASE WHEN i % 10 = 0 THEN NULL ELSE 'window_ended' END
FROM generate_series(1, 50000) AS i;

INSERT INTO events (id, intake, key, kind, trusted, title, labels, fingerprint, status, route, group_id, received_at)
SELECT md5('event' || i || '-' || n)::uuid, 'alertmanager', 'key-' || i || '-' || n, 'alert', true,
       'Alert ' || i, '{}', 'alert-' || i || '-' || n, 'grouped', 'default', md5('group' || i)::uuid,
       now() - make_interval(secs => i * 60 + n)
FROM generate_series(1, 50000) AS i, generate_series(1, 2) AS n;

ANALYZE;
