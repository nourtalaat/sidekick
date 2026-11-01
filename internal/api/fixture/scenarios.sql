-- A confirmed investigation to export as a scenario: the alert that opened
-- it came in on the alertmanager endpoint with a host name in its labels,
-- and it made two source calls, one of which answered with a host name, an
-- IP address and an email. The second event of the group is a Slack
-- mention, which the export must skip.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
VALUES ('0198f2a4-0000-7000-8000-0000000000b1', 'fp-scn', 'default', 'checkout-api', 'open', 2,
        '2026-05-04 10:20:00+00', '2026-05-04 10:21:00+00', '0198f2a4-0000-7000-8000-0000000000c1'),
       ('0198f2a4-0000-7000-8000-0000000000b2', 'fp-slack', 'default', 'checkout-api', 'open', 1,
        '2026-05-04 11:20:00+00', '2026-05-04 11:20:00+00', '0198f2a4-0000-7000-8000-0000000000c2');

INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, service, labels, fingerprint, status, route, group_id, raw, received_at)
VALUES ('0198f2a4-0000-7000-8000-0000000000a1', 'alertmanager', 'scn-1', 'alert', true, 'CheckoutLatency',
        'checkout-api latency above 2s', 'checkout-api', '{}', 'fp-scn', 'grouped', 'default',
        '0198f2a4-0000-7000-8000-0000000000b1',
        '{"status": "firing", "alerts": [{"status": "firing", "labels": {"alertname": "CheckoutLatency", "service": "checkout-api", "instance": "web-7.prod.acme.com:9100"}, "annotations": {"summary": "checkout-api latency above 2s"}, "startsAt": "2026-05-04T10:12:00Z"}]}',
        '2026-05-04 10:20:00+00'),
       ('0198f2a4-0000-7000-8000-0000000000a2', 'slack-alerts', 'scn-2', 'mention', true, NULL,
        'is checkout slow?', 'checkout-api', '{}', 'fp-slack', 'grouped', 'default',
        '0198f2a4-0000-7000-8000-0000000000b2', NULL, '2026-05-04 11:20:00+00');

INSERT INTO investigations (id, group_id, route, strategy, mode, status, run, next_seq, verdict, confidence, connectors, budget,
                            created_at, finished_at, alert_title, service, confirmed_verdict, confirmed_cause, confirmed_at)
VALUES ('0198f2a4-0000-7000-8000-0000000000c1', '0198f2a4-0000-7000-8000-0000000000b1', 'default', 'latency-alert', 'read-only',
        'final', 1, 10, 'true_positive', 'high', '{}', '{}', '2026-05-04 10:20:00+00', '2026-05-04 10:25:00+00',
        'checkout-api latency above 2s', 'checkout-api', 'true_positive',
        'Pool exhausted on web-7.prod.acme.com after db-1.prod.acme.com refused connections.', '2026-05-04 10:30:00+00'),
       ('0198f2a4-0000-7000-8000-0000000000c2', '0198f2a4-0000-7000-8000-0000000000b2', 'default', 'generic-alert', 'read-only',
        'final', 1, 10, 'true_positive', 'high', '{}', '{}', '2026-05-04 11:20:00+00', '2026-05-04 11:25:00+00',
        'is checkout slow?', 'checkout-api', 'true_positive', 'It was slow.', '2026-05-04 11:30:00+00'),
       ('0198f2a4-0000-7000-8000-0000000000c3', '0198f2a4-0000-7000-8000-0000000000b1', 'default', 'latency-alert', 'read-only',
        'final', 1, 10, 'true_positive', 'high', '{}', '{}', '2026-05-04 10:20:00+00', '2026-05-04 10:25:00+00',
        'not confirmed yet', 'checkout-api', NULL, NULL, NULL);

INSERT INTO steps (investigation_id, seq, run, kind, parts, source, tool, tool_call_id, status, started_at, duration_ms)
VALUES ('0198f2a4-0000-7000-8000-0000000000c1', 1, 1, 'model_turn',
        '[{"type": "tool_call", "id": "c1", "name": "signoz__search_logs", "input": {"query": "pool timeout", "service": "checkout-api"}}]',
        NULL, NULL, NULL, 'ok', '2026-05-04 10:21:00+00', 10),
       ('0198f2a4-0000-7000-8000-0000000000c1', 2, 1, 'tool_result',
        '[{"type": "tool_result", "call_id": "c1", "content": "<untrusted_data source=\"signoz\" tool=\"signoz__search_logs\" call=\"c1\">\n10:10:04 ERROR pq: timeout dialing db-1.prod.acme.com (10.1.2.3) user=alice@acme.com\n</untrusted_data>"}]',
        'signoz', 'search_logs', 'c1', 'ok', '2026-05-04 10:21:01+00', 10),
       ('0198f2a4-0000-7000-8000-0000000000c1', 3, 1, 'model_turn',
        '[{"type": "tool_call", "id": "c2", "name": "k8s__get_pod_events", "input": {"namespace": "prod"}}]',
        NULL, NULL, NULL, 'ok', '2026-05-04 10:22:00+00', 10),
       ('0198f2a4-0000-7000-8000-0000000000c1', 4, 1, 'tool_result',
        '[{"type": "tool_result", "call_id": "c2", "content": "The source is unavailable.", "is_error": true}]',
        'k8s', 'get_pod_events', 'c2', 'unavailable', '2026-05-04 10:22:01+00', 10);
