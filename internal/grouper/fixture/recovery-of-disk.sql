-- The recovery of the seeded group's only alert.
INSERT INTO events (id, intake, key, kind, trusted, alert_name, service, labels, fingerprint, status, route, group_id, received_at)
VALUES ('0192d3a4-0000-7000-8000-000000000009', 'grafana', 'disk-1-resolved', 'resolved', false, 'DiskFull', 'db',
        '{}', 'fp-disk', 'grouped', 'default', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 09:20:00+00');
