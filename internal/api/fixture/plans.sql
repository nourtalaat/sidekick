-- A large database for the query plan tests of the investigation list:
-- 100,000 investigations, one every minute, back from now, each with a
-- group. They have 200 routes, 500 services and 100 models; one in 100
-- failed, one in 200 has the verdict flapping, and 50 titles have the
-- word zebra. Each has an event and a delivered inbox item; 10,000 have
-- ratings of their verdict and 20,000 a watcher.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at)
SELECT md5('group' || i)::uuid, 'fp-' || i, 'route-' || (i % 200), 'closed', 1,
       now() - make_interval(secs => i * 60), now() - make_interval(secs => i * 60)
FROM generate_series(1, 100000) AS i;

INSERT INTO events (id, intake, key, kind, trusted, fingerprint, status, group_id, received_at)
SELECT md5('event' || i)::uuid, 'alertmanager', 'key-' || i, 'alert', true, 'fp-' || i, 'grouped', md5('group' || i)::uuid,
       now() - make_interval(secs => i * 60)
FROM generate_series(1, 100000) AS i;

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, verdict, confidence, run, created_at,
                            started_at, finished_at, cost_micros, alert_title, service, model)
SELECT md5('investigation' || i)::uuid, md5('group' || i)::uuid, 'route-' || (i % 200), 'read-only',
       CASE WHEN i % 100 = 0 THEN 'failed' ELSE 'final' END, '{}', '{}',
       CASE WHEN i % 100 = 0 THEN NULL WHEN i % 200 = 7 THEN 'flapping' WHEN i % 3 = 0 THEN 'true_positive' ELSE 'false_positive' END,
       CASE WHEN i % 100 = 0 THEN NULL ELSE 'high' END, 1,
       now() - make_interval(secs => i * 60), now() - make_interval(secs => i * 60 - 5), now() - make_interval(secs => i * 60 - 65),
       1000, 'Alert ' || i || ' latency' || CASE WHEN i % 2000 = 5 THEN ' zebra' ELSE '' END,
       'svc-' || (i % 500), 'model-' || (i % 100)
FROM generate_series(1, 100000) AS i;

INSERT INTO investigation_inbox (investigation_id, kind, body, created_at, delivered_seq)
SELECT md5('investigation' || i)::uuid, 'new_event', '{}', now() - make_interval(secs => i * 60), 1
FROM generate_series(1, 100000) AS i;

INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, slack_user_id, channel, created_at, updated_at)
SELECT md5('feedback' || i)::uuid, md5('investigation' || i)::uuid, 'verdict', 'run:1/verdict',
       CASE WHEN i % 20 = 0 THEN 'wrong' ELSE 'correct' END, 'U' || i, 'slack', now(), now()
FROM generate_series(1, 100000, 10) AS i;

INSERT INTO watchers (id, group_id, investigation_id, name, origin, check_type, spec, interval_s, passes_to_recover, check_timeout_s,
                      status, started_at, deadline)
SELECT md5('watcher' || i)::uuid, md5('group' || i)::uuid, md5('investigation' || i)::uuid, 'health', 'model', 'alert', '{}',
       30, 1, 10, 'recovered', now() - make_interval(secs => i * 60), now() - make_interval(secs => i * 60 - 3600)
FROM generate_series(1, 100000, 5) AS i;

INSERT INTO watcher_checks (watcher_id, seq, at, result, duration_ms)
SELECT md5('watcher' || i)::uuid, s, now() - make_interval(secs => i * 60 - s * 30), 'pass', 1
FROM generate_series(1, 100000, 5) AS i, generate_series(1, 5) AS s;

ANALYZE;
