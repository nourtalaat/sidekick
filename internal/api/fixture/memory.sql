-- Four memory entries for the tests of the memory endpoints. The last digit
-- of an entry's ID is its number, newest confirmation first.
--
--   e1  checkout-api, true positive, with a cause, enabled
--   e2  checkout-api, flapping, no cause, enabled
--   e3  orders-api, false positive, disabled
--   e4  no service or alert name, duplicate
INSERT INTO memory_entries (investigation_id, service, alert_name, fingerprint, verdict, summary, cause, confirmed_at, disabled)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000e1', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout', 'true_positive',
   'p99 latency rose after a deploy.', 'Deploy 4f2c1a added a slow query.', '2026-10-22 10:00:00+00', false),
  ('0192d3a4-0000-7000-8000-0000000000e2', 'checkout-api', 'CheckoutErrors', 'fp-errors', 'flapping',
   'Errors came and went for ten minutes.', NULL, '2026-10-21 10:00:00+00', false),
  ('0192d3a4-0000-7000-8000-0000000000e3', 'orders-api', 'OrdersSlow', 'fp-orders', 'false_positive',
   'Only the batch export endpoint was slow.', NULL, '2026-10-20 10:00:00+00', true),
  ('0192d3a4-0000-7000-8000-0000000000e4', NULL, NULL, '', 'duplicate',
   'Another alert of an open incident.', NULL, '2026-10-19 10:00:00+00', false);

-- Notes about two services, one written by the user Ad and one by a token.
INSERT INTO service_notes (service, text, updated_by, updated_at)
VALUES
  ('checkout-api', 'Checkout pages the on-call about pool exhaustion twice a week.', '01920000-0000-7000-8000-000000000040', '2026-10-20 10:00:00+00'),
  ('orders-api', 'Orders runs a nightly export.', NULL, '2026-10-21 10:00:00+00');
