-- Memory entries for the example tests. Most tests match them against an
-- alert with fingerprint fp-checkout, service checkout-api and alert name
-- CheckoutLatencyP99.
INSERT INTO memory_entries (investigation_id, service, alert_name, fingerprint, verdict, summary, cause, confirmed_at, disabled)
VALUES
  -- The same fingerprint.
  ('0192d3a4-0000-7000-8000-0000000000e1', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout', 'true_positive',
   'p99 latency rose after a deploy.', 'Deploy 4f2c1a added a slow query.', '2026-10-20 10:00:00+00', false),
  ('0192d3a4-0000-7000-8000-0000000000e2', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout', 'flapping',
   'p99 latency crossed the threshold and fell back every few minutes.', NULL, '2026-10-22 10:00:00+00', false),
  -- The same service and alert name, another fingerprint.
  ('0192d3a4-0000-7000-8000-0000000000e4', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout-eu', 'true_positive',
   'p99 latency rose in the EU region when a node pool ran out of memory.', 'Node pool eu-2 out of memory.',
   '2026-10-21 10:00:00+00', false),
  ('0192d3a4-0000-7000-8000-0000000000e5', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout-us', 'true_positive',
   'Disabled: p99 latency rose in the US region.', NULL, '2026-10-23 10:00:00+00', true),
  -- Only words in common.
  ('0192d3a4-0000-7000-8000-0000000000e6', 'orders-api', 'OrdersSlow', 'fp-orders', 'false_positive',
   'Latency was high only on the batch export endpoint.', NULL, '2026-10-19 10:00:00+00', false),
  ('0192d3a4-0000-7000-8000-0000000000e7', 'billing', 'DiskFull', 'fp-billing-disk', 'true_positive',
   'The disk filled with debug logs.', 'Debug logging left on after an incident.', '2026-10-18 10:00:00+00', false),
  ('0192d3a4-0000-7000-8000-0000000000e8', 'billing', 'DiskFull', 'fp-billing-disk-2', 'true_positive',
   'Disabled: the disk filled with debug logs again.', NULL, '2026-10-24 10:00:00+00', true),
  -- Disabled, and would match on every tier.
  ('0192d3a4-0000-7000-8000-0000000000e3', 'ledger', 'LedgerLag', 'fp-ledger', 'true_positive',
   'Ledger lag grew while the replica was rebuilt.', NULL, '2026-10-25 10:00:00+00', true);
