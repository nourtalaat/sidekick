-- Memory entries and service notes for the memory source tests.
INSERT INTO memory_entries (investigation_id, service, alert_name, fingerprint, verdict, summary, cause, confirmed_at, disabled)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000e1', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout', 'true_positive',
   'p99 latency rose after a deploy.', 'Deploy 4f2c1a added a slow query.', '2026-10-20 10:00:00+00', false),
  ('0192d3a4-0000-7000-8000-0000000000e2', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout', 'flapping',
   'p99 latency crossed the threshold and fell back every few minutes.', NULL, '2026-10-22 10:00:00+00', false),
  ('0192d3a4-0000-7000-8000-0000000000e3', 'orders-api', 'OrdersSlow', 'fp-orders', 'false_positive',
   'Latency was high only on the batch export endpoint.', NULL, '2026-10-19 10:00:00+00', false),
  ('0192d3a4-0000-7000-8000-0000000000e4', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout-us', 'true_positive',
   'Disabled: p99 latency rose in the US region.', NULL, '2026-10-23 10:00:00+00', true);

INSERT INTO service_notes (service, text, updated_at)
VALUES ('checkout-api', 'Latency alerts during the 02:00 batch run are expected.', '2026-10-01 09:00:00+00');
