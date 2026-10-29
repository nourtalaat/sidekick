-- Eight investigations in three hours, with known counts. All times are UTC.
--
-- 2026-10-20 10:00 (route r1, service checkout, model m1 unless noted):
--   c1 false_positive, final, 120 s long, first finding after 40 s, 5 tool
--      calls, 2000 micros, 100 in, 50 out, confirmed false_positive, fix
--      applied. Its watcher timed out.
--   c2 false_positive, final, 120 s, first finding 60 s, 3 calls, 1000
--      micros, 60 in, 30 out, confirmed true_positive. One watcher
--      recovered, one is still running, so the group has not recovered.
--   c3 true_positive, final, 300 s, first finding 30 s, 10 calls, 5000
--      micros, 200 in, 100 out, confirmed true_positive. Its group's
--      first event starts at 10:38 and its watcher recovered at 10:50, so
--      the downtime is 12 minutes.
--   c4 inconclusive on payments with m2, failed, 60 s, no first finding,
--      1 call, 500 micros, 10 in, 5 out, no feedback.
-- 2026-10-20 11:00:
--   c5 true_positive on r2, final, 300 s, first finding 20 s, 4 calls, 3000
--      micros, 80 in, 40 out, strategy deep, confirmed false_positive. Two
--      watchers recovered, at 11:20 and 11:25; the first event came at
--      11:09 with no start time, so the downtime is 16 minutes.
--   c6 on r2, still running with no verdict, service or model, 2 calls, 700
--      micros, 20 in, 10 out.
--   c7 false_positive, final, 120 s, first finding 100 s, 6 calls, 2500
--      micros, 90 in, 45 out, rated wrong without a real verdict.
-- 2026-10-21 09:00:
--   c8 false_positive, final, 180 s, first finding 30 s, 2 calls, 800
--      micros, 40 in, 20 out, no feedback.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-1', 'r1', 'closed', 1, '2026-10-20 10:05:00+00', '2026-10-20 10:05:00+00', '0192d3a4-0000-7000-8000-0000000000c1'),
  ('0192d3a4-0000-7000-8000-0000000000b2', 'fp-2', 'r1', 'closed', 1, '2026-10-20 10:20:00+00', '2026-10-20 10:20:00+00', '0192d3a4-0000-7000-8000-0000000000c2'),
  ('0192d3a4-0000-7000-8000-0000000000b3', 'fp-3', 'r1', 'closed', 1, '2026-10-20 10:40:00+00', '2026-10-20 10:40:00+00', '0192d3a4-0000-7000-8000-0000000000c3'),
  ('0192d3a4-0000-7000-8000-0000000000b4', 'fp-4', 'r1', 'closed', 1, '2026-10-20 10:50:00+00', '2026-10-20 10:50:00+00', '0192d3a4-0000-7000-8000-0000000000c4'),
  ('0192d3a4-0000-7000-8000-0000000000b5', 'fp-5', 'r2', 'closed', 1, '2026-10-20 11:09:00+00', '2026-10-20 11:09:00+00', '0192d3a4-0000-7000-8000-0000000000c5'),
  ('0192d3a4-0000-7000-8000-0000000000b6', 'fp-6', 'r2', 'open', 1, '2026-10-20 11:30:00+00', '2026-10-20 11:30:00+00', '0192d3a4-0000-7000-8000-0000000000c6'),
  ('0192d3a4-0000-7000-8000-0000000000b7', 'fp-7', 'r1', 'closed', 1, '2026-10-20 11:40:00+00', '2026-10-20 11:40:00+00', '0192d3a4-0000-7000-8000-0000000000c7'),
  ('0192d3a4-0000-7000-8000-0000000000b8', 'fp-8', 'r1', 'closed', 1, '2026-10-21 09:00:00+00', '2026-10-21 09:00:00+00', '0192d3a4-0000-7000-8000-0000000000c8');

INSERT INTO investigations (
  id, group_id, route, strategy, mode, status, verdict, connectors, budget, service, model,
  tool_calls, cost_micros, input_tokens, output_tokens,
  created_at, started_at, first_finding_at, finished_at, confirmed_verdict, fix_applied_at
)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'r1', 'default', 'read-only', 'final', 'false_positive', '{}', '{}', 'checkout', 'm1',
   5, 2000, 100, 50,
   '2026-10-20 10:05:00+00', '2026-10-20 10:05:10+00', '2026-10-20 10:05:40+00', '2026-10-20 10:07:10+00', 'false_positive', '2026-10-20 10:30:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c2', '0192d3a4-0000-7000-8000-0000000000b2', 'r1', 'default', 'read-only', 'final', 'false_positive', '{}', '{}', 'checkout', 'm1',
   3, 1000, 60, 30,
   '2026-10-20 10:20:00+00', '2026-10-20 10:20:00+00', '2026-10-20 10:21:00+00', '2026-10-20 10:22:00+00', 'true_positive', NULL),
  ('0192d3a4-0000-7000-8000-0000000000c3', '0192d3a4-0000-7000-8000-0000000000b3', 'r1', 'default', 'read-only', 'final', 'true_positive', '{}', '{}', 'checkout', 'm1',
   10, 5000, 200, 100,
   '2026-10-20 10:40:00+00', '2026-10-20 10:40:00+00', '2026-10-20 10:40:30+00', '2026-10-20 10:45:00+00', 'true_positive', NULL),
  ('0192d3a4-0000-7000-8000-0000000000c4', '0192d3a4-0000-7000-8000-0000000000b4', 'r1', NULL, 'read-only', 'failed', 'inconclusive', '{}', '{}', 'payments', 'm2',
   1, 500, 10, 5,
   '2026-10-20 10:50:00+00', '2026-10-20 10:50:00+00', NULL, '2026-10-20 10:51:00+00', NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000c5', '0192d3a4-0000-7000-8000-0000000000b5', 'r2', 'deep', 'read-only', 'final', 'true_positive', '{}', '{}', 'checkout', 'm1',
   4, 3000, 80, 40,
   '2026-10-20 11:10:00+00', '2026-10-20 11:10:00+00', '2026-10-20 11:10:20+00', '2026-10-20 11:15:00+00', 'false_positive', NULL),
  ('0192d3a4-0000-7000-8000-0000000000c6', '0192d3a4-0000-7000-8000-0000000000b6', 'r2', NULL, 'read-only', 'running', NULL, '{}', '{}', NULL, NULL,
   2, 700, 20, 10,
   '2026-10-20 11:30:00+00', '2026-10-20 11:30:00+00', NULL, NULL, NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000c7', '0192d3a4-0000-7000-8000-0000000000b7', 'r1', 'default', 'read-only', 'final', 'false_positive', '{}', '{}', 'checkout', 'm1',
   6, 2500, 90, 45,
   '2026-10-20 11:40:00+00', '2026-10-20 11:40:00+00', '2026-10-20 11:41:40+00', '2026-10-20 11:42:00+00', NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000c8', '0192d3a4-0000-7000-8000-0000000000b8', 'r1', 'default', 'read-only', 'final', 'false_positive', '{}', '{}', 'checkout', 'm1',
   2, 800, 40, 20,
   '2026-10-21 09:00:00+00', '2026-10-21 09:00:00+00', '2026-10-21 09:00:30+00', '2026-10-21 09:03:00+00', NULL, NULL);

INSERT INTO events (id, intake, key, kind, trusted, fingerprint, status, group_id, starts_at, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a3', 'grafana', 'ev-3', 'alert', false, 'fp-3', 'grouped', '0192d3a4-0000-7000-8000-0000000000b3', '2026-10-20 10:38:00+00', '2026-10-20 10:40:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a5', 'grafana', 'ev-5', 'alert', false, 'fp-5', 'grouped', '0192d3a4-0000-7000-8000-0000000000b5', NULL, '2026-10-20 11:09:00+00');

INSERT INTO watchers (id, group_id, investigation_id, name, origin, check_type, spec, interval_s, passes_to_recover,
                      check_timeout_s, status, started_at, deadline, recovered_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000d1', '0192d3a4-0000-7000-8000-0000000000b1', '0192d3a4-0000-7000-8000-0000000000c1',
   'health', 'model', 'alert', '{}', 30, 2, 10, 'timed_out', '2026-10-20 10:06:00+00', '2026-10-20 11:06:00+00', NULL),
  ('0192d3a4-0000-7000-8000-0000000000d2', '0192d3a4-0000-7000-8000-0000000000b2', '0192d3a4-0000-7000-8000-0000000000c2',
   'health', 'model', 'alert', '{}', 30, 2, 10, 'recovered', '2026-10-20 10:21:00+00', '2026-10-20 11:21:00+00', '2026-10-20 10:40:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000d3', '0192d3a4-0000-7000-8000-0000000000b2', '0192d3a4-0000-7000-8000-0000000000c2',
   'api', 'model', 'http', '{}', 30, 2, 10, 'running', '2026-10-20 10:21:00+00', '2026-10-20 11:21:00+00', NULL),
  ('0192d3a4-0000-7000-8000-0000000000d4', '0192d3a4-0000-7000-8000-0000000000b3', '0192d3a4-0000-7000-8000-0000000000c3',
   'health', 'model', 'alert', '{}', 30, 2, 10, 'recovered', '2026-10-20 10:41:00+00', '2026-10-20 11:41:00+00', '2026-10-20 10:50:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000d5', '0192d3a4-0000-7000-8000-0000000000b5', '0192d3a4-0000-7000-8000-0000000000c5',
   'health', 'model', 'alert', '{}', 30, 2, 10, 'recovered', '2026-10-20 11:11:00+00', '2026-10-20 12:11:00+00', '2026-10-20 11:20:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000d6', '0192d3a4-0000-7000-8000-0000000000b5', NULL,
   'api', 'route', 'http', '{}', 30, 2, 10, 'recovered', '2026-10-20 11:11:00+00', '2026-10-20 12:11:00+00', '2026-10-20 11:25:00+00');

INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, slack_user_id, channel, created_at, updated_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000e1', '0192d3a4-0000-7000-8000-0000000000c7', 'verdict', 'run:1/verdict', 'wrong', 'U1', 'slack', '2026-10-20 12:00:00+00', '2026-10-20 12:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000e2', '0192d3a4-0000-7000-8000-0000000000c1', 'verdict', 'run:1/verdict', 'correct', 'U1', 'slack', '2026-10-20 12:00:00+00', '2026-10-20 12:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000e3', '0192d3a4-0000-7000-8000-0000000000c8', 'finding', 'run:1/finding:f1', 'wrong', 'U1', 'slack', '2026-10-21 10:00:00+00', '2026-10-21 10:00:00+00');
