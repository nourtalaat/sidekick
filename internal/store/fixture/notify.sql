-- One running investigation with its group, for the tests of the
-- investigation_updates notifications.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at)
VALUES ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-1', 'default', 'open', 1, '2026-10-26 14:00:00+00', '2026-10-26 14:00:00+00');

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
VALUES ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'default', 'read-only', 'running', '{}', '{}', '2026-10-26 14:00:01+00');
