-- A busy database for the query plan tests: 5,000 groups with two events
-- and ten watchers each, one watcher in a hundred running, and 100,000
-- watcher checks.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, window_until, closed_reason)
SELECT md5('group' || i)::uuid, 'fp-' || i, 'default', 'closed', 2,
       now() - make_interval(secs => i * 60), now() - make_interval(secs => i * 60),
       now() - make_interval(secs => i * 60) + interval '30 minutes', 'window_ended'
FROM generate_series(1, 5000) AS i;

INSERT INTO events (id, intake, key, kind, trusted, title, labels, fingerprint, status, route, group_id, received_at)
SELECT md5('event' || i || '-' || n)::uuid, 'alertmanager', 'key-' || i || '-' || n, 'alert', true,
       'Alert ' || i, '{}', 'alert-' || i || '-' || n, 'grouped', 'default', md5('group' || i)::uuid,
       now() - make_interval(secs => i * 60 + n)
FROM generate_series(1, 5000) AS i, generate_series(1, 2) AS n;

INSERT INTO watchers (id, group_id, name, origin, check_type, spec, interval_s, passes_to_recover, check_timeout_s,
                      status, passes_in_a_row, checks, started_at, deadline)
SELECT md5('watcher' || i || '-' || n)::uuid, md5('group' || i)::uuid, 'watcher-' || n, 'route', 'alert', '{}', 30, 1, 10,
       CASE WHEN (i * 10 + n) % 100 = 0 THEN 'running' ELSE 'recovered' END, 0, 2,
       now() - make_interval(secs => i * 60), now() - make_interval(secs => i * 60) + interval '1 hour'
FROM generate_series(1, 5000) AS i, generate_series(1, 10) AS n;

INSERT INTO watcher_checks (watcher_id, seq, at, result, detail, duration_ms)
SELECT md5('watcher' || i || '-' || n)::uuid, s, now() - make_interval(secs => i * 60 - s * 30), 'pass', '1 of 1 alerts recovered', 2
FROM generate_series(1, 5000) AS i, generate_series(1, 10) AS n, generate_series(1, 2) AS s;

ANALYZE;
