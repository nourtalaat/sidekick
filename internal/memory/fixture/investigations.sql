-- Investigations for the Sync tests. A later event of another service
-- joins the first group, to show that an entry takes the fields of the
-- event that opened the group.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-checkout', 'payments', 'checkout-api', 'open', 2,
   '2026-10-26 14:02:00+00', '2026-10-26 14:03:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000b2', 'fp-orders', 'payments', 'orders-api', 'open', 1,
   '2026-10-26 15:00:00+00', '2026-10-26 15:00:00+00');

INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, body, service, severity, labels, fingerprint,
                    status, route, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'alertmanager', 'checkout-1', 'alert', true,
   'CheckoutLatencyP99', 'checkout-api p99 latency above 2s', 'p99 is 2.6 s.', 'checkout-api', 'critical',
   '{}', 'fp-checkout', 'grouped', 'payments', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 14:02:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a2', 'alertmanager', 'checkout-2', 'alert', true,
   'PaymentsErrors', 'payments errors', NULL, 'payments-api', 'warning',
   '{}', 'fp-payments', 'grouped', 'payments', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 14:03:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a3', 'alertmanager', 'orders-1', 'alert', true,
   'OrdersSlow', 'orders-api slow', NULL, 'orders-api', 'warning',
   '{}', 'fp-orders', 'grouped', 'payments', '0192d3a4-0000-7000-8000-0000000000b2', '2026-10-26 15:00:00+00');

INSERT INTO investigations (id, group_id, route, strategy, mode, status, run, verdict, confidence, report, stop_reason,
                            connectors, budget, created_at, started_at, finished_at,
                            confirmed_verdict, confirmed_cause, confirmed_by_slack_user, confirmed_at)
VALUES
  -- Confirmed.
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'payments', 'generic-alert',
   'read-only', 'final', 1, 'true_positive', 'high',
   '{"verdict": "true_positive", "reason": "Errors after a deploy.", "summary": "p99 latency rose after deploy 4f2c1a.", "cause": "Deploy 4f2c1a"}',
   'submitted', '{}', '{}', '2026-10-26 14:02:01+00', '2026-10-26 14:02:02+00', '2026-10-26 14:04:00+00',
   'true_positive', 'Deploy 4f2c1a', 'U01ANA', '2026-10-26 14:10:00+00'),
  -- Confirmed, with a report that has no summary.
  ('0192d3a4-0000-7000-8000-0000000000c2', '0192d3a4-0000-7000-8000-0000000000b2', 'payments', 'generic-alert',
   'read-only', 'final', 1, 'false_positive', 'high',
   '{"verdict": "false_positive", "reason": "Only the batch export endpoint was slow."}',
   'submitted', '{}', '{}', '2026-10-26 15:00:01+00', '2026-10-26 15:00:02+00', '2026-10-26 15:04:00+00',
   'false_positive', NULL, 'U01BEN', '2026-10-26 15:10:00+00'),
  -- Not confirmed.
  ('0192d3a4-0000-7000-8000-0000000000c3', '0192d3a4-0000-7000-8000-0000000000b2', 'payments', 'generic-alert',
   'read-only', 'final', 2, 'true_positive', 'medium',
   '{"verdict": "true_positive", "reason": "Slow queries.", "summary": "Slow queries on orders-db."}',
   'submitted', '{}', '{}', '2026-10-26 15:20:01+00', '2026-10-26 15:20:02+00', '2026-10-26 15:24:00+00',
   NULL, NULL, NULL, NULL);
