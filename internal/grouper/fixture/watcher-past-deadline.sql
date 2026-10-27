-- A watcher of the seeded group that is still running past its deadline,
-- as one whose tick has not ended it yet.
INSERT INTO watchers (id, group_id, name, origin, check_type, spec, interval_s, passes_to_recover, check_timeout_s, status, started_at, deadline)
VALUES ('0192d3a4-0000-7000-8000-0000000000d3', '0192d3a4-0000-7000-8000-0000000000b1', 'late', 'route', 'http',
        '{"url": "http://127.0.0.1/healthz"}', 30, 2, 10, 'running', '2026-10-26 08:00:00+00', '2026-10-26 09:00:00+00');
