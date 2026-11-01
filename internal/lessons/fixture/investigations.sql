-- Two investigations of one event group for the lessons tests: one that ended
-- with a verdict, and one that has none yet.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-checkout', 'payments', 'checkout-api', 'open', 1,
   '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00');

INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, body, service, severity, labels, fingerprint,
                    status, route, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'alertmanager', 'checkout-1', 'alert', true,
   'CheckoutLatencyP99', 'checkout-api p99 latency above 2s', 'p99 is 2.6 s.', 'checkout-api', 'critical',
   '{"team": "payments"}', 'fp-checkout', 'routed', 'payments', '0192d3a4-0000-7000-8000-0000000000b1',
   '2026-10-26 14:02:00+00');

INSERT INTO investigations (id, group_id, route, strategy, mode, status, run, verdict, confidence, report, stop_reason,
                            connectors, budget, created_at, started_at, finished_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'payments', 'generic-alert',
   'read-only', 'final', 1, 'true_positive', 'high',
   '{"verdict": "true_positive", "confidence": "high", "reason": "Errors after a deploy.", "cause": "Deploy 4f2c1a",
     "findings": [{"id": "f1", "claim": "Errors rose at 14:02.", "evidence": []}],
     "fixes": [{"id": "x1", "title": "Roll back 4f2c1a", "detail": "Revert the deploy."}],
     "stopReason": "submitted"}',
   'submitted', '{}', '{}', '2026-10-26 14:02:01+00', '2026-10-26 14:02:02+00', '2026-10-26 14:04:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c2', '0192d3a4-0000-7000-8000-0000000000b1', 'payments', 'generic-alert',
   'read-only', 'running', 1, NULL, NULL, NULL, NULL, '{}', '{}', '2026-10-26 14:02:01+00', '2026-10-26 14:02:02+00', NULL);
