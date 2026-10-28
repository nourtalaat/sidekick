-- Four investigations as they were before the list columns existed (all
-- NULL), one of them with no title on its first event, and one that the
-- router created after (filled, with an empty title).
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-1', 'payments', 'checkout-api', 'open', 1, '2026-10-26 14:00:00+00', '2026-10-26 14:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000b2', 'fp-2', 'payments', NULL, 'open', 1, '2026-10-26 14:10:00+00', '2026-10-26 14:10:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000b3', 'fp-3', 'default', 'db', 'open', 1, '2026-10-26 14:20:00+00', '2026-10-26 14:20:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000b4', 'fp-4', 'default', 'db', 'open', 1, '2026-10-26 14:30:00+00', '2026-10-26 14:30:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000b5', 'fp-5', 'default', 'api', 'open', 1, '2026-10-26 14:40:00+00', '2026-10-26 14:40:00+00');

-- Group b1 has two events; the title comes from the first to arrive.
INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, body, service, severity, labels, fingerprint, status, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'am', 'k1', 'alert', true, 'CheckoutLatency', 'p99 above 2s', NULL, 'checkout-api', 'critical', '{}', 'fp-1', 'grouped', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 14:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a2', 'am', 'k2', 'alert', true, 'CheckoutLatency', 'p99 above 3s', NULL, 'checkout-api', 'critical', '{}', 'fp-1', 'grouped', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 14:05:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a3', 'am', 'k3', 'alert', true, 'DiskFull', NULL, NULL, NULL, 'warning', '{}', 'fp-2', 'grouped', '0192d3a4-0000-7000-8000-0000000000b2', '2026-10-26 14:10:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a4', 'slack', 'k4', 'message', false, NULL, NULL, 'Why is the queue backed up?', NULL, 'info', '{}', 'fp-3', 'grouped', '0192d3a4-0000-7000-8000-0000000000b3', '2026-10-26 14:20:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a5', 'am', 'k5', 'alert', true, NULL, NULL, NULL, NULL, 'info', '{}', 'fp-4', 'grouped', '0192d3a4-0000-7000-8000-0000000000b4', '2026-10-26 14:30:00+00');

INSERT INTO investigations (id, group_id, route, strategy, strategy_version, mode, status, connectors, budget, created_at, alert_title, service, model)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'payments', 'generic-alert', 'v', 'read-only', 'final', '{}', '{}', '2026-10-26 14:00:01+00', NULL, NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000c2', '0192d3a4-0000-7000-8000-0000000000b2', 'payments', 'generic-alert', 'v', 'read-only', 'final', '{}', '{}', '2026-10-26 14:10:01+00', NULL, NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000c3', '0192d3a4-0000-7000-8000-0000000000b3', 'default', 'generic-alert', 'v', 'read-only', 'queued', '{}', '{}', '2026-10-26 14:20:01+00', NULL, NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000c4', '0192d3a4-0000-7000-8000-0000000000b4', 'default', 'generic-alert', 'v', 'read-only', 'queued', '{}', '{}', '2026-10-26 14:30:01+00', NULL, NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000c5', '0192d3a4-0000-7000-8000-0000000000b5', 'default', 'generic-alert', 'v', 'read-only', 'queued', '{}', '{}', '2026-10-26 14:40:01+00', '', 'api', NULL);

-- Investigation c1 had a triage turn, then two model turns of different
-- models; the first model turn names its model.
INSERT INTO steps (investigation_id, seq, run, kind, parts, status, model, started_at, duration_ms)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', 0, 1, 'triage_turn', '[]', 'ok', 'small-model', '2026-10-26 14:00:02+00', 10),
  ('0192d3a4-0000-7000-8000-0000000000c1', 1, 1, 'prompt', '{}', 'ok', NULL, '2026-10-26 14:00:03+00', 10),
  ('0192d3a4-0000-7000-8000-0000000000c1', 2, 1, 'model_turn', '[]', 'ok', 'big-model', '2026-10-26 14:00:04+00', 10),
  ('0192d3a4-0000-7000-8000-0000000000c1', 3, 1, 'model_turn', '[]', 'ok', 'other-model', '2026-10-26 14:00:05+00', 10);
