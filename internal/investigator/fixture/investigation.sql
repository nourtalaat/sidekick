-- One queued investigation as the router leaves it, for the investigator
-- tests: its event, its group, its publish state with a Slack output, and
-- its investigate job. The tests set the budget they need.
INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, body, service, severity, labels, fingerprint,
                    status, route, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'alertmanager', 'checkout-1', 'alert', true,
   'CheckoutLatencyP99', 'checkout-api p99 latency above 2s', 'p99 is 2.6 s.', 'checkout-api', 'critical',
   '{"team": "payments"}', 'fp-checkout', 'routed', 'payments', '0192d3a4-0000-7000-8000-0000000000b1',
   '2026-10-26 14:02:00+00');

INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-checkout', 'payments', 'checkout-api', 'open', 1,
   '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00', '0192d3a4-0000-7000-8000-0000000000c1');

INSERT INTO investigations (id, group_id, route, strategy, strategy_version, mode, status, connectors, budget, created_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'payments', 'generic-alert',
   'stored-version', 'read-only', 'queued',
   '{"investigate": {"connector": "claude", "model": "claude-opus-5-5"}}',
   '{"max_tool_calls": 40, "max_wall_time_ms": 600000}', '2026-10-26 14:02:01+00');

INSERT INTO publish_states (investigation_id, sinks, seq, phase, body, updated_at)
VALUES ('0192d3a4-0000-7000-8000-0000000000c1', '{slack-thread}', 0, 'queued', '{}', '2026-10-26 14:02:01+00');

INSERT INTO jobs (kind, route, payload, unique_key)
VALUES ('investigate', 'payments',
        '{"investigation_id": "0192d3a4-0000-7000-8000-0000000000c1", "group_id": "0192d3a4-0000-7000-8000-0000000000b1"}',
        'investigate:0192d3a4-0000-7000-8000-0000000000b1');
