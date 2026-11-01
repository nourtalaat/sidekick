-- Three final investigations that ran checkout-latency at runtime:1, whose
-- verdicts people can rate wrong. Their IDs are
-- 01920000-0000-7000-8000-00000000010N.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
SELECT ('01920000-0000-7000-8000-00000000020' || n)::uuid, 'fp-' || n, 'payments', 'checkout-api', 'closed', 1,
       '2026-10-26 10:00:00+00', '2026-10-26 10:00:00+00', ('01920000-0000-7000-8000-00000000010' || n)::uuid
FROM generate_series(1, 3) AS n;

INSERT INTO investigations (id, group_id, route, strategy, strategy_version, mode, status, run, verdict, confidence, report,
                            stop_reason, connectors, budget, created_at, started_at, finished_at)
SELECT ('01920000-0000-7000-8000-00000000010' || n)::uuid, ('01920000-0000-7000-8000-00000000020' || n)::uuid, 'payments',
       'checkout-latency', 'runtime:1', 'read-only', 'final', 1, 'true_positive', 'high',
       '{"verdict": "true_positive", "summary": "A summary"}', 'submitted', '{}', '{}',
       '2026-10-26 10:00:00+00', '2026-10-26 10:00:01+00', '2026-10-26 10:02:00+00'
FROM generate_series(1, 3) AS n;
