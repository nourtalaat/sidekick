-- For the grouper tests: an open group of fp-disk with its investigation
-- and publish state, a routed event that fits it, and a routed event of
-- another fingerprint.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, window_until, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-disk', 'default', 'db', 'open', 1,
   '2026-10-26 09:00:00+00', '2026-10-26 09:00:00+00', '2026-10-26 09:30:00+00', '0192d3a4-0000-7000-8000-0000000000c1');

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'default', 'read-only', 'running',
   '{}', '{}', '2026-10-26 09:00:01+00');

INSERT INTO publish_states (investigation_id, sinks, seq, update_seq, phase, body, updated_at)
VALUES ('0192d3a4-0000-7000-8000-0000000000c1', '{slack-thread}', 3, 3, 'running', '{}', '2026-10-26 09:01:00+00');

INSERT INTO events (id, intake, key, kind, trusted, alert_name, service, labels, fingerprint, status, route, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-000000000001', 'grafana', 'disk-1', 'alert', false, 'DiskFull', 'db',
   '{}', 'fp-disk', 'grouped', 'default', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 09:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000002', 'grafana', 'disk-2', 'alert', false, 'DiskFull', 'db',
   '{}', 'fp-disk', 'routed', 'default', NULL, '2026-10-26 09:10:00+00'),
  ('0192d3a4-0000-7000-8000-000000000003', 'grafana', 'cpu-1', 'alert', false, 'HighCPU', 'db',
   '{"env": "prod"}', 'fp-cpu', 'routed', 'default', NULL, '2026-10-26 09:10:00+00');
