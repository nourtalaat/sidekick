-- Six investigations for the Slack target tests:
--   c1: the alert is a message in a channel, and two Slack outputs published
--       threads for it, the first in the same channel and a later one in
--       another (a Teams output published one too).
--   c2: the alert is a reply in a thread, and no output published.
--   c3: the alert came from a webhook, and a Slack output published a thread.
--   c4: the alert came from a webhook, and nothing published.
--   c5: the alert came from a webhook, nothing published, and a person
--       mentioned Sidekick in Slack later, which joined the group.
--   c6: a person posted the alert in a listener's channel.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-1', 'payments', 'checkout-api', 'open', 1, '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00', '0192d3a4-0000-7000-8000-0000000000c1'),
  ('0192d3a4-0000-7000-8000-0000000000b2', 'fp-2', 'payments', 'checkout-api', 'open', 1, '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00', '0192d3a4-0000-7000-8000-0000000000c2'),
  ('0192d3a4-0000-7000-8000-0000000000b3', 'fp-3', 'payments', 'checkout-api', 'open', 1, '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00', '0192d3a4-0000-7000-8000-0000000000c3'),
  ('0192d3a4-0000-7000-8000-0000000000b4', 'fp-4', 'payments', 'checkout-api', 'open', 1, '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00', '0192d3a4-0000-7000-8000-0000000000c4'),
  ('0192d3a4-0000-7000-8000-0000000000b5', 'fp-5', 'payments', 'checkout-api', 'open', 2, '2026-10-26 14:02:00+00', '2026-10-26 14:09:00+00', '0192d3a4-0000-7000-8000-0000000000c5'),
  ('0192d3a4-0000-7000-8000-0000000000b6', 'fp-6', 'payments', 'checkout-api', 'open', 1, '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00', '0192d3a4-0000-7000-8000-0000000000c6');

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at, alert_title)
SELECT ('0192d3a4-0000-7000-8000-0000000000c' || n)::uuid, ('0192d3a4-0000-7000-8000-0000000000b' || n)::uuid,
       'payments', 'approved', 'running', '{}', '{"max_tool_calls": 40, "max_wall_time_ms": 600000}', '2026-10-26 14:02:01+00', 'Alert ' || n
FROM generate_series(1, 6) AS n;

INSERT INTO events (id, intake, key, kind, trusted, fingerprint, slack, status, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'alerts-channel', 'k1', 'alert', true, 'fp-1',
   '{"TeamID": "T1", "Channel": "C0123456789", "TS": "1700000000.000100", "ThreadTS": "", "UserID": "U1"}', 'grouped',
   '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 14:02:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a2', 'alerts-channel', 'k2', 'alert', true, 'fp-2',
   '{"TeamID": "T1", "Channel": "C0123456789", "TS": "1700000500.000200", "ThreadTS": "1700000400.000100", "UserID": "U1"}', 'grouped',
   '0192d3a4-0000-7000-8000-0000000000b2', '2026-10-26 14:02:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a3', 'alertmanager', 'k3', 'alert', true, 'fp-3', NULL, 'grouped',
   '0192d3a4-0000-7000-8000-0000000000b3', '2026-10-26 14:02:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a4', 'alertmanager', 'k4', 'alert', true, 'fp-4', NULL, 'grouped',
   '0192d3a4-0000-7000-8000-0000000000b4', '2026-10-26 14:02:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a5', 'alertmanager', 'k5', 'alert', true, 'fp-5', NULL, 'grouped',
   '0192d3a4-0000-7000-8000-0000000000b5', '2026-10-26 14:02:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a7', 'mentions', 'k7', 'mention', false, 'fp-5',
   '{"TeamID": "T1", "Channel": "C0000000004", "TS": "1700000800.000400", "ThreadTS": "", "UserID": "U2"}', 'grouped',
   '0192d3a4-0000-7000-8000-0000000000b5', '2026-10-26 14:09:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a6', 'alerts-channel', 'k6', 'message', true, 'fp-6',
   '{"TeamID": "T1", "Channel": "C0123456789", "TS": "1700000700.000300", "ThreadTS": "", "UserID": "U3"}', 'grouped',
   '0192d3a4-0000-7000-8000-0000000000b6', '2026-10-26 14:02:00+00');

INSERT INTO output_refs (investigation_id, sink, external, ref, last_seq, published_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', 'slack-thread', 'C0123456789:1700000000.000100', '{}', 1, '2026-10-26 14:03:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c1', 'teams-channel', '19:abc@thread.tacv2;messageid=5', '{}', 1, '2026-10-26 14:04:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c1', 'slack-oncall', 'C0000000003:1700000600.000500', '{}', 1, '2026-10-26 14:05:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c3', 'slack-thread', 'C0000000002:1700000900.000300', '{}', 1, '2026-10-26 14:03:00+00');
