-- 20,000 investigations, each with its group, one event and 25 steps
-- (500,000 steps), and a year of daily spend, for the query plan tests. Investigation i has the ID
-- md5('inv' || i) and fence 1000 + i.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
SELECT md5('grp' || i)::uuid, 'fp-' || i, 'payments', 'checkout-api', 'open', 1,
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       md5('inv' || i)::uuid
FROM generate_series(1, 20000) AS i;

INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, body, service, severity, labels, fingerprint,
                    status, route, group_id, received_at)
SELECT md5('evt' || i)::uuid, 'alertmanager', 'key-' || i, 'alert', true, 'CheckoutLatencyP99', 'p99 above 2s',
       'p99 is 2.6 s.', 'checkout-api', 'critical', '{}', 'fp-' || i, 'routed', 'payments', md5('grp' || i)::uuid,
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute'
FROM generate_series(1, 20000) AS i;

INSERT INTO investigations (id, group_id, route, strategy, strategy_version, mode, status, next_seq, fence,
                            connectors, budget, created_at, started_at)
SELECT md5('inv' || i)::uuid, md5('grp' || i)::uuid, 'payments', 'generic-alert', 'v1', 'read-only',
       CASE WHEN i % 10 = 0 THEN 'running' ELSE 'final' END, 25, 1000 + i,
       '{"investigate": {"connector": "claude", "model": "claude-opus-5-5"}}',
       '{"max_tool_calls": 40, "max_wall_time_ms": 600000}',
       '2026-10-26 00:00:00+00'::timestamptz + i * interval '1 minute',
       '2026-10-26 00:00:01+00'::timestamptz + i * interval '1 minute'
FROM generate_series(1, 20000) AS i;

INSERT INTO steps (investigation_id, seq, run, kind, parts, source, tool, tool_call_id, status, started_at, duration_ms)
SELECT md5('inv' || i)::uuid, s, 1, 'tool_result',
       '[{"type": "tool_result", "call_id": "c1", "content": "error rate 5% at 14:02"}]',
       'signoz', 'search_logs', 'c' || s, 'ok',
       '2026-10-26 00:00:02+00'::timestamptz + i * interval '1 minute', 120
FROM generate_series(1, 20000) AS i, generate_series(0, 24) AS s;

-- A year of daily spend for 20 routes on 3 models: 21,900 rows.
INSERT INTO spend_daily (day, route, connector, model, cost_micros, input_tokens, output_tokens)
SELECT '2026-10-26'::date - d, 'route-' || r, 'claude', 'model-' || m, 10500 * r, 1000 * r, 200 * r
FROM generate_series(0, 364) AS d, generate_series(1, 20) AS r, generate_series(1, 3) AS m;

ANALYZE;
