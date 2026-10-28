-- A large database for the query plan tests: 100,000 investigations from
-- before the list columns existed, each with a group, an event and a model
-- turn, and 100 that are filled.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at)
SELECT md5('group' || i)::uuid, 'fp-' || i, 'default', 'svc-' || (i % 50), 'closed', 1,
       now() - make_interval(secs => i * 60), now() - make_interval(secs => i * 60)
FROM generate_series(1, 100000) AS i;

INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, fingerprint, status, group_id, received_at)
SELECT md5('event' || i)::uuid, 'alertmanager', 'key-' || i, 'alert', true, 'Alert' || i, 'Title ' || i,
       'fp-' || i, 'grouped', md5('group' || i)::uuid, now() - make_interval(secs => i * 60)
FROM generate_series(1, 100000) AS i;

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at, alert_title)
SELECT md5('investigation' || i)::uuid, md5('group' || i)::uuid, 'default', 'read-only', 'final', '{}', '{}',
       now() - make_interval(secs => i * 60), CASE WHEN i <= 100 THEN 'Title ' || i END
FROM generate_series(1, 100000) AS i;

INSERT INTO steps (investigation_id, seq, run, kind, parts, status, model, started_at, duration_ms)
SELECT md5('investigation' || i)::uuid, 0, 1, 'model_turn', '[]', 'ok', 'model-' || (i % 5),
       now() - make_interval(secs => i * 60), 1
FROM generate_series(1, 100000) AS i;

ANALYZE;
