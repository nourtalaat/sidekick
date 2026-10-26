-- Two queued investigations as the router leaves them, for the publish
-- tests: one for a webhook alert and one for an alert posted in Slack.
-- Publish states are written by the tests with Open.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-checkout', 'payments', 'checkout-api', 'open', 1,
   '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00', '0192d3a4-0000-7000-8000-0000000000c1'),
  ('0192d3a4-0000-7000-8000-0000000000b2', 'fp-disk', 'default', 'db', 'open', 2,
   '2026-10-26 14:05:00+00', '2026-10-26 14:06:00+00', '0192d3a4-0000-7000-8000-0000000000c2');

INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, body, service, severity, labels, links, slack,
                    fingerprint, status, route, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'alertmanager', 'checkout-1', 'alert', true,
   'CheckoutLatencyP99', 'checkout-api p99 latency above 2s', 'p99 is 2.6 s.', 'checkout-api', 'critical',
   '{"team": "payments"}', '[{"title": "Runbook", "url": "https://runbooks.example.com/checkout"}]', NULL,
   'fp-checkout', 'routed', 'payments', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 14:02:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a2', 'slack-alerts', 'disk-1', 'alert', false,
   'DiskFull', 'Disk almost full', NULL, 'db', 'warning',
   '{}', NULL, '{"TeamID": "T1", "Channel": "C999", "TS": "1793000000.000001", "ThreadTS": "", "UserID": "U1"}',
   'fp-disk', 'routed', 'default', '0192d3a4-0000-7000-8000-0000000000b2', '2026-10-26 14:05:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a3', 'slack-alerts', 'disk-2', 'alert', false,
   'DiskFull', 'Disk almost full again', NULL, 'db', 'warning',
   '{}', NULL, NULL,
   'fp-disk', 'routed', 'default', '0192d3a4-0000-7000-8000-0000000000b2', '2026-10-26 14:06:00+00');

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'payments', 'read-only', 'queued',
   '{}', '{"max_tool_calls": 40, "max_wall_time_ms": 600000}', '2026-10-26 14:02:01+00'),
  ('0192d3a4-0000-7000-8000-0000000000c2', '0192d3a4-0000-7000-8000-0000000000b2', 'default', 'read-only', 'queued',
   '{}', '{"max_tool_calls": 40, "max_wall_time_ms": 600000}', '2026-10-26 14:05:01+00');
