-- A busy database for the query plan tests: 50,000 investigations, each
-- with a group of two events, a publish state and a Slack output ref, on
-- top of fixture/investigations.sql.
INSERT INTO event_groups (id, fingerprint, route, status, opened_at, last_event_at, investigation_id)
SELECT md5('group' || i)::uuid, 'fp-' || i, 'default', 'open', now(), now(), md5('inv' || i)::uuid
FROM generate_series(1, 50000) AS i;

INSERT INTO events (id, intake, key, kind, trusted, title, labels, fingerprint, status, route, group_id, received_at)
SELECT md5('event' || i || '-' || n)::uuid, 'alertmanager', 'key-' || i || '-' || n, 'alert', true,
       'Alert ' || i, '{}', 'fp-' || i, 'routed', 'default', md5('group' || i)::uuid,
       now() - make_interval(secs => i * 10 + n)
FROM generate_series(1, 50000) AS i, generate_series(1, 2) AS n;

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
SELECT md5('inv' || i)::uuid, md5('group' || i)::uuid, 'default', 'read-only', 'running', '{}', '{}', now()
FROM generate_series(1, 50000) AS i;

INSERT INTO publish_states (investigation_id, sinks, seq, phase, body, updated_at)
SELECT md5('inv' || i)::uuid, ARRAY['slack-thread'], 5, 'running', '{"progress": [{"at": "2026-10-26T14:02:30Z", "text": "Read the alert"}]}', now()
FROM generate_series(1, 50000) AS i;

INSERT INTO output_refs (investigation_id, sink, external, ref, last_seq, published_at)
SELECT md5('inv' || i)::uuid, 'slack-thread', 'C123:1793023321.' || lpad(i::text, 6, '0'),
       '{"sink": "slack-thread", "external": "C123:1793023321.000001", "data": {"seq": "4"}}', 4, now()
FROM generate_series(1, 50000) AS i;

ANALYZE;
