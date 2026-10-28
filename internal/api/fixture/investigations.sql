-- Five investigations for the tests of the investigation endpoints:
--
--   c1  payments, checkout-api, final true_positive with a report, a
--       confirmation, ratings, two events, and a recovered watcher
--   c2  payments, db, final false_positive, one wrong rating
--   c3  default, checkout-api, running, with steps of two runs, a waiting
--       follow-up question and a running watcher
--   c4  default, no service, failed, no title
--   c5  default, db, queued; created at the same time as c4
--
-- Times are on 2026-10-26, in UTC.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-1', 'payments', 'checkout-api', 'open', 2, '2026-10-26 14:00:00+00', '2026-10-26 14:05:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000b2', 'fp-2', 'payments', 'db', 'closed', 1, '2026-10-26 14:10:00+00', '2026-10-26 14:10:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000b3', 'fp-3', 'default', 'checkout-api', 'open', 1, '2026-10-26 14:20:00+00', '2026-10-26 14:20:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000b4', 'fp-4', 'default', NULL, 'closed', 1, '2026-10-26 14:30:00+00', '2026-10-26 14:30:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000b5', 'fp-5', 'default', 'db', 'open', 1, '2026-10-26 14:30:00+00', '2026-10-26 14:30:00+00');

INSERT INTO events (id, intake, key, kind, sender_kind, sender_name, trusted, alert_name, title, body, service, severity,
                    labels, links, fingerprint, status, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'alertmanager', 'k1', 'alert', 'bot', 'Alertmanager', true, 'CheckoutLatency',
   'Checkout p99 latency above 2s', 'p99 is 2.6 s.', 'checkout-api', 'critical',
   '{"team": "payments", "env": "prod"}',
   '[{"title": "Runbook", "url": "https://runbooks.example.com/checkout"}, {"title": "Bad", "url": "javascript:alert(1)"}]',
   'fp-1', 'grouped', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 14:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a2', 'alertmanager', 'k2', 'alert', NULL, NULL, true, 'CheckoutLatency',
   'Checkout p99 latency above 3s', repeat('x', 3000), 'checkout-api', 'critical',
   '{}', NULL, 'fp-1', 'grouped', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 14:05:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a3', 'alertmanager', 'k3', 'alert', NULL, NULL, true, 'DiskFull',
   'Database disk almost full', NULL, 'db', 'warning',
   '{}', NULL, 'fp-2', 'grouped', '0192d3a4-0000-7000-8000-0000000000b2', '2026-10-26 14:10:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a4', 'alertmanager', 'k4', 'alert', NULL, NULL, true, 'CheckoutErrors',
   'Checkout error rate high', NULL, 'checkout-api', 'critical',
   '{}', NULL, 'fp-3', 'grouped', '0192d3a4-0000-7000-8000-0000000000b3', '2026-10-26 14:20:00+00');

INSERT INTO investigations (id, group_id, route, strategy, strategy_version, mode, status, run, next_seq, verdict, confidence, report,
                            stop_reason, connectors, budget, input_tokens, output_tokens, cost_micros, tool_calls, created_at,
                            started_at, first_finding_at, finished_at, confirmed_verdict, confirmed_cause, confirmed_at,
                            alert_title, service, model)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'payments', 'generic-alert', 'v1', 'read-only',
   'final', 2, 0, 'true_positive', 'high',
   '{"verdict": "true_positive", "confidence": "high", "reason": "A deploy caused it.", "summary": "Deploy 4f2c1a doubled latency.",
     "cause": "Deploy 4f2c1a", "impact": "Checkout is slow.",
     "findings": [
       {"id": "f1", "claim": "Errors rose at 14:02.",
        "evidence": [{"kind": "metric", "title": "SigNoz", "url": "https://signoz.example.com/d/1", "toolCallId": "c1"},
                     {"kind": "other", "title": "Local", "url": "javascript:alert(1)"}]},
       {"id": "f2", "claim": "The deploy caused it.", "evidence": null}],
     "fixes": [{"id": "x1", "title": "Roll back 4f2c1a", "detail": "Revert the deploy."}],
     "checked": [{"source": "signoz", "status": "ok", "toolCalls": 1}],
     "stopReason": "submitted",
     "followUps": [{"run": 2, "questions": [{"author": "Ana", "text": "Was it the cache?"}], "answer": "No.", "answered": true,
                    "at": "2026-10-26T15:00:00Z"}]}',
   'submitted', '{"investigate": {"connector": "claude", "model": "big-model"}}', '{}', 1200, 300, 12000, 4,
   '2026-10-26 14:00:01+00', '2026-10-26 14:00:05+00', '2026-10-26 14:02:00+00', '2026-10-26 14:03:05+00',
   'true_positive', 'Deploy', '2026-10-26 14:30:00+00',
   'Checkout p99 latency above 2s', 'checkout-api', 'big-model'),
  ('0192d3a4-0000-7000-8000-0000000000c2', '0192d3a4-0000-7000-8000-0000000000b2', 'payments', 'generic-alert', 'v1', 'read-only',
   'final', 1, 0, 'false_positive', 'medium', NULL, 'submitted', '{"investigate": {"connector": "claude"}}', '{}', 0, 0, 3000, 1,
   '2026-10-26 14:10:01+00', '2026-10-26 14:10:05+00', NULL, '2026-10-26 14:11:05+00', NULL, NULL, NULL,
   'Database disk almost full', 'db', 'other-model'),
  ('0192d3a4-0000-7000-8000-0000000000c3', '0192d3a4-0000-7000-8000-0000000000b3', 'default', 'generic-alert', 'v1', 'read-only',
   'running', 2, 5, NULL, NULL, NULL, NULL, '{}', '{}', 0, 0, 500, 1,
   '2026-10-26 14:20:01+00', '2026-10-26 14:20:05+00', NULL, NULL, NULL, NULL, NULL,
   'Checkout error rate high', 'checkout-api', 'big-model'),
  ('0192d3a4-0000-7000-8000-0000000000c4', '0192d3a4-0000-7000-8000-0000000000b4', 'default', 'generic-alert', 'v1', 'read-only',
   'failed', 1, 0, NULL, NULL, NULL, 'error', '{}', '{}', 0, 0, 0, 0,
   '2026-10-26 14:30:01+00', '2026-10-26 14:30:05+00', NULL, '2026-10-26 14:31:05+00', NULL, NULL, NULL,
   '', NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000c5', '0192d3a4-0000-7000-8000-0000000000b5', 'default', 'generic-alert', 'v1', 'read-only',
   'queued', 1, 0, NULL, NULL, NULL, NULL, '{}', '{}', 0, 0, 0, 0,
   '2026-10-26 14:30:01+00', NULL, NULL, NULL, NULL, NULL, NULL,
   'Queue backed up', 'db', NULL);

-- Ratings of the verdict of c1: run 1's wrong rating is history, and run 2's
-- two correct ones and one wrong one count.
INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, slack_user_id, channel, created_at, updated_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000e1', '0192d3a4-0000-7000-8000-0000000000c1', 'verdict', 'run:1/verdict', 'wrong', 'U1', 'slack', now(), now()),
  ('0192d3a4-0000-7000-8000-0000000000e2', '0192d3a4-0000-7000-8000-0000000000c1', 'verdict', 'run:2/verdict', 'correct', 'U1', 'slack', now(), now()),
  ('0192d3a4-0000-7000-8000-0000000000e3', '0192d3a4-0000-7000-8000-0000000000c1', 'verdict', 'run:2/verdict', 'correct', 'U2', 'slack', now(), now()),
  ('0192d3a4-0000-7000-8000-0000000000e4', '0192d3a4-0000-7000-8000-0000000000c1', 'verdict', 'run:2/verdict', 'wrong', 'U3', 'slack', now(), now()),
  ('0192d3a4-0000-7000-8000-0000000000e5', '0192d3a4-0000-7000-8000-0000000000c2', 'verdict', 'run:1/verdict', 'wrong', 'U1', 'slack', now(), now());

INSERT INTO watchers (id, group_id, investigation_id, name, origin, check_type, spec, interval_s, passes_to_recover, check_timeout_s,
                      status, passes_in_a_row, checks, started_at, deadline, last_check_at, ended_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000d1', '0192d3a4-0000-7000-8000-0000000000b1', '0192d3a4-0000-7000-8000-0000000000c1',
   'health', 'model', 'http', '{}', 30, 2, 10, 'recovered', 2, 3,
   '2026-10-26 14:01:00+00', '2026-10-26 15:01:00+00', '2026-10-26 14:02:30+00', '2026-10-26 14:02:30+00'),
  ('0192d3a4-0000-7000-8000-0000000000d2', '0192d3a4-0000-7000-8000-0000000000b3', '0192d3a4-0000-7000-8000-0000000000c3',
   'health', 'route', 'alert', '{}', 30, 1, 10, 'running', 0, 1,
   '2026-10-26 14:21:00+00', '2026-10-26 15:21:00+00', '2026-10-26 14:21:00+00', NULL);

INSERT INTO watcher_checks (watcher_id, seq, at, result, detail, duration_ms)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000d1', 1, '2026-10-26 14:01:00+00', 'fail', 'HTTP 503', 12),
  ('0192d3a4-0000-7000-8000-0000000000d1', 2, '2026-10-26 14:02:00+00', 'pass', 'HTTP 200', 15),
  ('0192d3a4-0000-7000-8000-0000000000d1', 3, '2026-10-26 14:02:30+00', 'pass', 'HTTP 200', 11),
  ('0192d3a4-0000-7000-8000-0000000000d2', 1, '2026-10-26 14:21:00+00', 'fail', 'alert still firing', 3);

INSERT INTO investigation_inbox (investigation_id, kind, body, key, created_at, delivered_seq)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c3', 'follow_up', '{"text": "Was it the cache?", "author": "Ana"}', 'q1', '2026-10-26 14:25:00+00', NULL),
  ('0192d3a4-0000-7000-8000-0000000000c3', 'follow_up', '{"text": "Already answered."}', 'q0', '2026-10-26 14:22:00+00', 3),
  ('0192d3a4-0000-7000-8000-0000000000c3', 'new_event', '{}', NULL, '2026-10-26 14:23:00+00', NULL);

-- The steps of c3: the prompt, a model turn with reasoning and a tool call,
-- its result, a note, and a model turn of run 2.
INSERT INTO steps (investigation_id, seq, run, kind, parts, source, tool, tool_call_id, status, connector, model, input_tokens,
                   output_tokens, cache_read_tokens, cache_write_tokens, cost_micros, started_at, duration_ms)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c3', 0, 1, 'prompt',
   '{"system": [{"type": "text", "text": "SYSTEM SECRET"}], "tools": [{"name": "search_logs"}],
     "parts": [{"type": "text", "text": "Investigate this alert."}]}',
   NULL, NULL, NULL, 'ok', NULL, NULL, NULL, NULL, NULL, NULL, NULL, '2026-10-26 14:20:05+00', 1),
  ('0192d3a4-0000-7000-8000-0000000000c3', 1, 1, 'model_turn',
   '[{"type": "reasoning", "provider": "anthropic", "text": "Thinking about logs.", "signature": "sig-secret"},
     {"type": "reasoning", "provider": "anthropic", "signature": "only-a-signature"},
     {"type": "text", "text": "I will search."},
     {"type": "tool_call", "id": "c1", "name": "search_logs", "input": {"query": "level:error"}}]',
   NULL, NULL, NULL, 'ok', 'claude', 'big-model', 100, 20, 10, 5, 500, '2026-10-26 14:20:06+00', 900),
  ('0192d3a4-0000-7000-8000-0000000000c3', 2, 1, 'tool_result',
   '[{"type": "tool_result", "call_id": "c1", "content": "3 errors", "is_error": false}]',
   'signoz', 'search_logs', 'c1', 'ok', NULL, NULL, NULL, NULL, NULL, NULL, NULL, '2026-10-26 14:20:08+00', 120),
  ('0192d3a4-0000-7000-8000-0000000000c3', 3, 1, 'note',
   '[{"type": "text", "text": "Reminder."}]',
   NULL, NULL, NULL, 'ok', NULL, NULL, NULL, NULL, NULL, NULL, NULL, '2026-10-26 14:20:09+00', 1),
  ('0192d3a4-0000-7000-8000-0000000000c3', 4, 2, 'model_turn',
   '[{"type": "text", "text": "Follow-up answer."}]',
   NULL, NULL, NULL, 'ok', 'claude', 'big-model', 50, 10, NULL, NULL, NULL, '2026-10-26 14:26:00+00', 400);
