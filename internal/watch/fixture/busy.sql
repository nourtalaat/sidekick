-- A busy database for the query plan tests: 5,000 groups with two events
-- and ten watchers each, one watcher in a hundred running, 100,000
-- watcher checks, 1,000 users and 100 API tokens.
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

-- The users and API tokens that a stopped watcher's name is read from.
INSERT INTO users (id, oidc_issuer, oidc_subject, name, role, created_at, last_login_at)
SELECT md5('user' || i)::uuid, 'https://idp.example.com', 'user-' || i, 'User ' || i, 'responder', now(), now()
FROM generate_series(1, 1000) AS i;

INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at)
SELECT md5('token' || i)::uuid, 'token ' || i, sha256(convert_to('skt_' || i, 'UTF8')), 'viewer', md5('user1')::uuid, now(), now() + interval '30 days'
FROM generate_series(1, 100) AS i;

ANALYZE;
