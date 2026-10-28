-- A large database for the query plan tests: 50,000 closed groups with an
-- investigation and a watcher each over the last 400 days, 1,000,000
-- steps, 100,000 events, 100,000 watcher checks, 100,000 audit log
-- entries over 800 days, and 120,000 finished jobs.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, closed_reason, investigation_id)
SELECT md5('group' || i)::uuid, 'fp-' || i, 'default', 'closed', 2,
       now() - make_interval(secs => i * 691), now() - make_interval(secs => i * 691), 'window_ended',
       md5('investigation' || i)::uuid
FROM generate_series(1, 50000) AS i;

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
SELECT md5('investigation' || i)::uuid, md5('group' || i)::uuid, 'default', 'read-only', 'final', '{}', '{}',
       now() - make_interval(secs => i * 691)
FROM generate_series(1, 50000) AS i;

INSERT INTO steps (investigation_id, seq, run, kind, parts, status, started_at, duration_ms)
SELECT md5('investigation' || i)::uuid, s, 1, 'model_turn', '[]', 'ok',
       now() - make_interval(secs => i * 691 - s), 1
FROM generate_series(1, 50000) AS i, generate_series(0, 19) AS s;

INSERT INTO investigation_inbox (investigation_id, kind, body, created_at, delivered_seq)
SELECT md5('investigation' || i)::uuid, 'new_event', '{}', now() - make_interval(secs => i * 691 - 1), 1
FROM generate_series(1, 50000) AS i;

INSERT INTO watchers (id, group_id, investigation_id, name, origin, check_type, spec, interval_s, passes_to_recover,
                      check_timeout_s, status, started_at, deadline)
SELECT md5('watcher' || i)::uuid, md5('group' || i)::uuid, md5('investigation' || i)::uuid, 'health', 'model', 'alert',
       '{}', 30, 2, 10, 'recovered', now() - make_interval(secs => i * 691), now() - make_interval(secs => i * 691 - 3600)
FROM generate_series(1, 50000) AS i;

INSERT INTO watcher_checks (watcher_id, seq, at, result, duration_ms)
SELECT md5('watcher' || i)::uuid, s, now() - make_interval(secs => i * 691 - s * 30), 'pass', 1
FROM generate_series(1, 50000) AS i, generate_series(1, 2) AS s;

INSERT INTO events (id, intake, key, kind, trusted, fingerprint, status, group_id, received_at)
SELECT md5('event' || i || '-' || n)::uuid, 'alertmanager', 'key-' || i || '-' || n, 'alert', true,
       'fp-' || i, 'grouped', md5('group' || i)::uuid, now() - make_interval(secs => i * 691 - n)
FROM generate_series(1, 50000) AS i, generate_series(1, 2) AS n;

INSERT INTO audit_log (at, actor_kind, action)
SELECT now() - make_interval(secs => i * 691), 'system', 'route.updated'
FROM generate_series(1, 100000) AS i;

INSERT INTO jobs (kind, payload, status, finished_at)
SELECT 'publish', '{}', 'done', now() - make_interval(secs => i * 3)
FROM generate_series(1, 100000) AS i;

INSERT INTO jobs (kind, payload, status, attempts, last_error, finished_at)
SELECT 'publish', '{}', 'dead', 5, 'fixture error', now() - make_interval(secs => i * 259)
FROM generate_series(1, 20000) AS i;

INSERT INTO jobs (kind, payload, unique_key, status, finished_at)
SELECT 'retention', '{}', 'retention:' || to_char(now() - make_interval(days => i), 'YYYY-MM-DD'), 'done',
       now() - make_interval(days => i)
FROM generate_series(1, 2) AS i;

ANALYZE;
