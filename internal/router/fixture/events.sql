-- Stored events as intake leaves them, for the router tests.
INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, body, service, severity, labels, fingerprint, status, skip_reason, received_at)
VALUES
  -- Fits the payments-latency route.
  ('0192d3a4-0000-7000-8000-000000000001', 'alertmanager', 'payments-1', 'alert', true,
   'CheckoutLatency', 'High p99 latency', 'Checkout is slow.', 'checkout-api', 'critical',
   '{"team": "payments", "env": "prod"}', 'fp-checkout', 'received', NULL, '2026-10-26 09:00:00+00'),
  -- Two alerts with the same fingerprint from an untrusted intake.
  ('0192d3a4-0000-7000-8000-000000000002', 'grafana', 'disk-1', 'alert', false,
   'DiskFull', 'Disk almost full', NULL, 'db', 'warning',
   '{}', 'fp-disk', 'received', NULL, '2026-10-26 09:01:00+00'),
  ('0192d3a4-0000-7000-8000-000000000003', 'grafana', 'disk-2', 'alert', false,
   'DiskFull', 'Disk almost full', NULL, 'db', 'warning',
   '{}', 'fp-disk', 'received', NULL, '2026-10-26 09:02:00+00'),
  -- A resolved alert, which intake stores as skipped.
  ('0192d3a4-0000-7000-8000-000000000004', 'alertmanager', 'payments-1-resolved', 'resolved', true,
   'CheckoutLatency', 'High p99 latency', NULL, 'checkout-api', 'critical',
   '{"team": "payments"}', 'fp-checkout', 'skipped', 'resolved events are linked in M1', '2026-10-26 09:10:00+00'),
  -- A message that no route but the default fits.
  ('0192d3a4-0000-7000-8000-000000000005', 'alertmanager', 'other-1', 'alert', true,
   NULL, NULL, NULL, NULL, 'info',
   '{}', 'fp-other', 'received', NULL, '2026-10-26 09:20:00+00');
