-- 20,000 confirmed investigations, each with its group, its event and its
-- memory entry, and 1,000 services with notes, for the query plan tests.
-- Investigation i has the ID md5('inv' || i). Entries share a fingerprint
-- in groups of 10, a service in groups of 200 and a service and alert name
-- in groups of 200, and each summary has a word, w<n>, that 40 entries
-- share. Every tenth entry is disabled.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
SELECT md5('grp' || i)::uuid, 'fp-' || (i % 2000), 'payments', 'svc-' || (i % 100), 'closed', 1,
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       md5('inv' || i)::uuid
FROM generate_series(1, 20000) AS i;

INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, service, labels, fingerprint, status, route,
                    group_id, received_at)
SELECT md5('evt' || i)::uuid, 'alertmanager', 'key-' || i, 'alert', true, 'Alert' || (i % 50), 'title ' || i,
       'svc-' || (i % 100), '{}', 'fp-' || (i % 2000), 'grouped', 'payments', md5('grp' || i)::uuid,
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(1, 20000) AS i;

INSERT INTO investigations (id, group_id, route, strategy, mode, status, run, verdict, confidence, report, stop_reason,
                            connectors, budget, created_at, started_at, finished_at,
                            confirmed_verdict, confirmed_cause, confirmed_by_slack_user, confirmed_at)
SELECT md5('inv' || i)::uuid, md5('grp' || i)::uuid, 'payments', 'generic-alert', 'read-only', 'final', 1,
       'true_positive', 'high', jsonb_build_object('verdict', 'true_positive', 'summary', 'Summary w' || (i % 500)),
       'submitted', '{}', '{}',
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:00:01+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:02:00+00'::timestamptz + i * interval '1 minute',
       'true_positive', 'Deploy ' || i, 'U01ANA',
       '2026-10-26 00:03:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(1, 20000) AS i;

INSERT INTO memory_entries (investigation_id, service, alert_name, fingerprint, verdict, summary, cause, confirmed_at, disabled)
SELECT md5('inv' || i)::uuid, 'svc-' || (i % 100), 'Alert' || (i % 50), 'fp-' || (i % 2000),
       CASE WHEN i % 4 = 0 THEN 'false_positive' ELSE 'true_positive' END,
       'Summary w' || (i % 500), 'Deploy ' || i,
       '2026-10-26 00:03:00+00'::timestamptz + i * interval '1 minute',
       i % 10 = 0
FROM generate_series(1, 20000) AS i;

INSERT INTO service_notes (service, text, updated_at)
SELECT 'svc-' || i, 'Notes about svc-' || i, '2026-10-26 00:00:00+00'
FROM generate_series(1, 1000) AS i;

ANALYZE event_groups, events, investigations, memory_entries, service_notes;
