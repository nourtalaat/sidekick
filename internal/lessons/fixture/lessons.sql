-- Lessons for the tests of the ranking, for the investigation of the alert
-- CheckoutLatencyP99 of the service checkout-api on the route payments,
-- with the strategy latency-alert. The last 12 digits of a lesson's ID are
-- its number. Each one says here what makes it rank where it does.
INSERT INTO lessons (id, text, scope_service, scope_alert, scope_route, scope_strategy, status, origin, helpful, unhelpful, created_at)
VALUES
  -- All four scope fields: the most specific.
  ('0192d3a4-0000-7000-8000-000000000014', 'Look at the pool settings of the last deploy.', 'checkout-api', 'CheckoutLatencyP99', 'payments', 'latency-alert', 'active', 'ui', 0, 0, '2026-10-02 00:00:00+00'),
  -- Two fields each. Same helpful count and no words in common with the
  -- event, so the newer one (16) comes first.
  ('0192d3a4-0000-7000-8000-000000000001', 'Look at recent deploys first.', 'checkout-api', 'CheckoutLatencyP99', NULL, NULL, 'active', 'ui', 0, 0, '2026-10-03 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000016', 'Compare traffic with yesterday.', NULL, 'CheckoutLatencyP99', 'payments', NULL, 'active', 'ui', 0, 0, '2026-10-04 00:00:00+00'),
  -- One field each. 2 and 3 have the best helpful minus unhelpful counts.
  ('0192d3a4-0000-7000-8000-000000000002', 'Ask the payments team about holiday traffic.', 'checkout-api', NULL, NULL, NULL, 'active', 'ui', 4, 1, '2026-10-01 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000003', 'Compare with the last release notes.', 'checkout-api', NULL, NULL, NULL, 'active', 'ui', 1, 0, '2026-10-01 00:00:00+00'),
  -- Net count 0, the oldest of the group, but 21 shares words with the event.
  ('0192d3a4-0000-7000-8000-000000000021', 'Latency above 2s usually means pool exhaustion in the database.', 'checkout-api', NULL, NULL, NULL, 'active', 'ui', 0, 0, '2026-10-01 00:00:00+00'),
  -- Net count 0 and no words in common: the newest comes first.
  ('0192d3a4-0000-7000-8000-000000000031', 'Check the status page of the card processor.', 'checkout-api', NULL, NULL, NULL, 'active', 'ui', 2, 2, '2026-10-09 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000032', 'Check whether the load balancer drained a node.', 'checkout-api', NULL, NULL, NULL, 'active', 'ui', 0, 0, '2026-10-08 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000007', 'Read the playbook of this strategy.', NULL, NULL, NULL, 'latency-alert', 'active', 'ui', 0, 0, '2026-10-07 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000006', 'Payments alerts go to the payments channel.', NULL, NULL, 'payments', NULL, 'active', 'ui', 0, 0, '2026-10-06 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000005', 'This alert fires during the nightly batch.', NULL, 'CheckoutLatencyP99', NULL, NULL, 'active', 'ui', 0, 0, '2026-10-05 00:00:00+00'),
  -- Global lessons come last, also 8, which has the best count of all.
  ('0192d3a4-0000-7000-8000-000000000008', 'Always say what you could not check.', NULL, NULL, NULL, NULL, 'active', 'ui', 9, 4, '2026-10-01 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000009', 'Prefer a short answer.', NULL, NULL, NULL, NULL, 'active', 'ui', 0, 0, '2026-10-10 00:00:00+00'),
  -- Lessons that do not apply: another service, an alert and a route that
  -- do not match, a strategy that does not match, and lessons that are not
  -- active.
  ('0192d3a4-0000-7000-8000-000000000010', 'Only for the search service.', 'search-api', NULL, NULL, NULL, 'active', 'ui', 0, 0, '2026-10-01 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000011', 'Right service, another alert.', 'checkout-api', 'CheckoutErrorRate', NULL, NULL, 'active', 'ui', 0, 0, '2026-10-01 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000015', 'Right route, another strategy.', NULL, NULL, 'payments', 'error-rate-alert', 'active', 'ui', 0, 0, '2026-10-01 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000012', 'Disabled.', 'checkout-api', NULL, NULL, NULL, 'disabled', 'ui', 0, 0, '2026-10-01 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000013', 'Waiting for review.', 'checkout-api', NULL, NULL, NULL, 'proposed', 'feedback', 0, 0, '2026-10-01 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000017', 'Rejected.', 'checkout-api', NULL, NULL, NULL, 'rejected', 'feedback', 0, 0, '2026-10-01 00:00:00+00');
