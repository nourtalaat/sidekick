-- A running HTTP watcher of the seeded group, far from its deadline.
INSERT INTO watchers (id, group_id, name, origin, check_type, spec, interval_s, passes_to_recover, check_timeout_s, status, started_at, deadline)
VALUES ('0192d3a4-0000-7000-8000-0000000000d1', '0192d3a4-0000-7000-8000-0000000000b1', 'health', 'route', 'http',
        '{"url": "http://127.0.0.1/healthz"}', 30, 2, 10, 'running', '2026-10-26 09:00:00+00', '2100-01-01 00:00:00+00');
