-- One investigation that has ended, for the follow-up tests: the person's
-- message in C01OPS it investigated, its group, and the ref of the Slack
-- output's message in the message's thread, 1761472960.000500.
INSERT INTO events (id, intake, key, kind, trusted, title, body, fingerprint, status, route, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000001a1', 'ops', 'Ev01OPS', 'message', false, 'Checkout returns 502',
   'Checkout returns 502', 'fp-ops', 'routed', 'default', '0192d3a4-0000-7000-8000-0000000001b1',
   '2026-10-26 14:02:40+00');

INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000001b1', 'fp-ops', 'default', 'open', 1,
   '2026-10-26 14:02:40+00', '2026-10-26 14:02:40+00', '0192d3a4-0000-7000-8000-0000000001c1');

INSERT INTO investigations (id, group_id, route, strategy, strategy_version, mode, status, connectors, budget,
                            created_at, started_at, finished_at, next_seq, verdict, confidence, report, stop_reason)
VALUES
  ('0192d3a4-0000-7000-8000-0000000001c1', '0192d3a4-0000-7000-8000-0000000001b1', 'default', 'generic-alert',
   'v1', 'read-only', 'final', '{"investigate": {"connector": "fake"}}', '{"max_tool_calls": 40, "max_wall_time_ms": 600000}',
   '2026-10-26 14:02:41+00', '2026-10-26 14:02:41+00', '2026-10-26 14:03:10+00', 4, 'inconclusive', 'low',
   '{"verdict": "inconclusive", "confidence": "low", "reason": "Nothing to check.", "summary": "", "findings": [], "checked": [], "stopReason": "submitted"}',
   'submitted');

INSERT INTO output_refs (investigation_id, sink, external, ref, last_seq, published_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000001c1', 'slack-thread', 'C01OPS:1761472960.000500',
   '{"sink": "slack-thread", "external": "C01OPS:1761472960.000500", "data": {"channel": "C01OPS", "ts": "1793023321.000001", "thread_ts": "1761472960.000500", "seq": "5"}}',
   5, '2026-10-26 14:03:11+00');
