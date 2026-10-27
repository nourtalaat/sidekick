-- For the related-service grouping tests: an open group of the orders-db
-- service with its investigation and publish state, and routed events of
-- other fingerprints: one of orders-db itself, one of checkout-api, which
-- depends on orders-db, one of an unrelated service and one without a
-- service.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, window_until, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000001b1', 'fp-db-latency', 'default', 'orders-db', 'open', 1,
   '2026-10-26 09:00:00+00', '2026-10-26 09:00:00+00', '2026-10-26 09:30:00+00', '0192d3a4-0000-7000-8000-0000000001c1');

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000001c1', '0192d3a4-0000-7000-8000-0000000001b1', 'default', 'read-only', 'running',
   '{}', '{}', '2026-10-26 09:00:01+00');

INSERT INTO publish_states (investigation_id, sinks, seq, update_seq, phase, body, updated_at)
VALUES ('0192d3a4-0000-7000-8000-0000000001c1', '{slack-thread}', 3, 3, 'running', '{}', '2026-10-26 09:01:00+00');

INSERT INTO events (id, intake, key, kind, trusted, alert_name, service, labels, fingerprint, status, route, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-000000000101', 'grafana', 'db-latency-1', 'alert', false, 'DBLatency', 'orders-db',
   '{}', 'fp-db-latency', 'grouped', 'default', '0192d3a4-0000-7000-8000-0000000001b1', '2026-10-26 09:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000102', 'grafana', 'db-connections-1', 'alert', false, 'DBConnections', 'orders-db',
   '{}', 'fp-db-connections', 'routed', 'default', NULL, '2026-10-26 09:05:00+00'),
  ('0192d3a4-0000-7000-8000-000000000103', 'grafana', 'checkout-errors-1', 'alert', false, 'HighErrorRate', 'checkout-api',
   '{}', 'fp-checkout-errors', 'routed', 'default', NULL, '2026-10-26 09:05:00+00'),
  ('0192d3a4-0000-7000-8000-000000000104', 'grafana', 'search-errors-1', 'alert', false, 'HighErrorRate', 'search',
   '{}', 'fp-search-errors', 'routed', 'default', NULL, '2026-10-26 09:05:00+00'),
  ('0192d3a4-0000-7000-8000-000000000105', 'grafana', 'disk-1', 'alert', false, 'DiskFull', NULL,
   '{}', 'fp-disk', 'routed', 'default', NULL, '2026-10-26 09:05:00+00');
