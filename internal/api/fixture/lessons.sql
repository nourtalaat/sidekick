-- Five lessons for the tests of the lesson endpoints, to load after
-- investigations.sql. The last digit of a lesson's ID is its number.
--
--   d1  waits for review, drafted from feedback f9 on c2, for checkout-api
--   d2  active, global, rated helpful 3 times and unhelpful once
--   d3  active, written in Slack, for checkout-api and CheckoutLatency
--   d4  disabled, written in the web UI, for db
--   d5  rejected, drafted from feedback, for the route payments
--
-- Investigation c1 used d2, d3 and d4, and its prompt step records them
-- and one lesson it left out.
INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, reason, user_id, channel, created_at, updated_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000f9', '0192d3a4-0000-7000-8000-0000000000c2', 'tool_call', 'call-lessons', 'wrong',
   'It searched the wrong service.', '01920000-0000-7000-8000-000000000030', 'ui', '2026-10-01 00:00:00+00', '2026-10-01 00:00:00+00');

INSERT INTO lessons (id, text, scope_service, scope_alert, scope_route, scope_strategy, status, origin, feedback_ids, created_by, helpful,
                     unhelpful, created_at, updated_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000d1', 'Search the service the alert names, not the route.', 'checkout-api', NULL, NULL, NULL, 'proposed',
   'feedback', ARRAY['0192d3a4-0000-7000-8000-0000000000f9']::uuid[], NULL, 0, 0, '2026-10-01 00:00:00+00', '2026-10-01 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000d2', 'Say what you could not check.', NULL, NULL, NULL, NULL, 'active', 'ui', '{}',
   '01920000-0000-7000-8000-000000000040', 3, 1, '2026-10-02 00:00:00+00', '2026-10-02 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000d3', 'Look at deploys of the last two hours first.', 'checkout-api', 'CheckoutLatency', NULL, NULL, 'active',
   'slack', '{}', NULL, 0, 0, '2026-10-03 00:00:00+00', '2026-10-03 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000d4', 'Disk alerts on db are often the nightly backup.', 'db', NULL, NULL, NULL, 'disabled', 'ui', '{}',
   '01920000-0000-7000-8000-000000000040', 0, 0, '2026-10-04 00:00:00+00', '2026-10-04 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000d5', 'Mention the on-call channel.', NULL, NULL, 'payments', NULL, 'rejected', 'feedback', '{}', NULL, 0, 0,
   '2026-10-05 00:00:00+00', '2026-10-05 00:00:00+00');

INSERT INTO lesson_uses (lesson_id, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000d2', '0192d3a4-0000-7000-8000-0000000000c1'),
  ('0192d3a4-0000-7000-8000-0000000000d3', '0192d3a4-0000-7000-8000-0000000000c1'),
  ('0192d3a4-0000-7000-8000-0000000000d4', '0192d3a4-0000-7000-8000-0000000000c1');

INSERT INTO steps (investigation_id, seq, run, kind, parts, status, started_at, duration_ms, lessons)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', 0, 1, 'prompt',
   '{"system": [{"type": "text", "text": "SYSTEM"}], "tools": [], "parts": [{"type": "text", "text": "Investigate this alert."}]}',
   'ok', '2026-10-26 14:00:05+00', 1,
   '{"used": ["0192d3a4-0000-7000-8000-0000000000d2", "0192d3a4-0000-7000-8000-0000000000d3"],
     "leftOut": [{"id": "0192d3a4-0000-7000-8000-0000000000d4", "reason": "max_tokens", "scope": {"service": "db"}}],
     "leftOutTotal": 1, "candidates": 3}');
