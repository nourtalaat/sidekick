-- Two investigations of one alert for the button tests: one that ended
-- with a true positive verdict and whose message is posted, and one that
-- waits for its investigate job.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-checkout', 'default', 'checkout-api', 'open', 1,
   '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00');

INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, body, service, severity, labels, fingerprint,
                    status, route, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'alerts', 'Ev01FIRING', 'alert', true,
   'HighErrorRate', '[FIRING] HighErrorRate', 'Error rate above 5%.', 'checkout', 'critical',
   '{"env": "prod"}', 'fp-checkout', 'routed', 'default', '0192d3a4-0000-7000-8000-0000000000b1',
   '2026-10-26 14:02:00+00');

INSERT INTO investigations (id, group_id, route, strategy, strategy_version, mode, status, run, verdict, confidence,
                            report, stop_reason, connectors, budget, created_at, started_at, finished_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'default', 'generic-alert',
   'stored-version', 'read-only', 'final', 1, 'true_positive', 'high',
   '{"verdict": "true_positive", "confidence": "high", "reason": "Errors after a deploy.", "cause": "Deploy 4f2c1a", "stopReason": "submitted"}',
   'submitted', '{"investigate": {"connector": "fake", "model": "claude-opus-5-5"}}',
   '{"max_tool_calls": 40, "max_wall_time_ms": 600000}', '2026-10-26 14:02:01+00', '2026-10-26 14:02:02+00',
   '2026-10-26 14:04:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c2', '0192d3a4-0000-7000-8000-0000000000b1', 'default', 'generic-alert',
   'stored-version', 'read-only', 'queued', 1, NULL, NULL, NULL, NULL,
   '{"investigate": {"connector": "fake", "model": "claude-opus-5-5"}}',
   '{"max_tool_calls": 40, "max_wall_time_ms": 600000}', '2026-10-26 14:05:01+00', NULL, NULL);

INSERT INTO publish_states (investigation_id, sinks, seq, phase, body, updated_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '{slack-thread}', 5, 'final',
   '{"report": {"verdict": "true_positive", "confidence": "high", "reason": "Errors after a deploy.", "stopReason": "submitted"}}',
   '2026-10-26 14:04:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c2', '{slack-thread}', 0, 'queued', '{}', '2026-10-26 14:05:01+00');

INSERT INTO jobs (kind, route, payload, unique_key)
VALUES ('investigate', 'default',
        '{"investigation_id": "0192d3a4-0000-7000-8000-0000000000c2", "group_id": "0192d3a4-0000-7000-8000-0000000000b1"}',
        'investigate:0192d3a4-0000-7000-8000-0000000000b1');
