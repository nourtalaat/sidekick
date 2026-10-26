-- Two more events in the group of fixture/investigation.sql, for the inbox
-- tests: another firing alert and the recovery of the first one.
INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, service, severity, labels, fingerprint,
                    status, route, group_id, ends_at, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a2', 'alertmanager', 'checkout-2', 'alert', true,
   'CheckoutLatencyP99', 'checkout-api p99 latency above 2s', 'checkout-api', 'critical',
   '{"team": "payments", "pod": "checkout-api-2"}', 'fp-checkout-2', 'grouped', 'payments',
   '0192d3a4-0000-7000-8000-0000000000b1', NULL, '2026-10-26 14:03:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a3', 'alertmanager', 'checkout-1-resolved', 'resolved', true,
   'CheckoutLatencyP99', 'checkout-api p99 latency above 2s', 'checkout-api', 'critical',
   '{"team": "payments"}', 'fp-checkout', 'grouped', NULL,
   '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 14:04:00+00', '2026-10-26 14:04:30+00');
