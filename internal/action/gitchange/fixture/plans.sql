-- A busy install for the query plan tests: 60,000 proposals of the
-- monitor-pr action over a year, 3 in 100 of them succeeded, with the
-- pull request state of 1,200 of those, the CI report of all of those, and
-- 100,000 finished jobs.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, closed_reason, investigation_id)
VALUES (md5('group')::uuid, 'fp', 'payments', 'closed', 2, now(), now(), 'window_ended', md5('investigation')::uuid);

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
VALUES (md5('investigation')::uuid, md5('group')::uuid, 'payments', 'approved', 'final', '{}', '{}', now());

INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key, proposed_at, expires_at, executed_at)
SELECT md5('proposal' || i)::uuid, md5('investigation')::uuid, 'monitor-pr', jsonb_build_object('repo', 'acme/monitors'), '', 'approved',
       CASE WHEN i % 100 < 3 THEN 'succeeded' ELSE 'rejected' END,
       'sidekick/' || md5('proposal' || i),
       now() - make_interval(secs => i * 500), now() - make_interval(secs => i * 500) + interval '1 day',
       CASE WHEN i % 100 < 3 THEN now() - make_interval(secs => i * 500) END
FROM generate_series(1, 60000) AS i;

-- The undo of a quarter of the succeeded proposals that ended on 1 in 100, half
-- of them with a revert pull request, which has a state.
INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key, proposed_at, expires_at, executed_at, undo_of)
SELECT md5('undo' || i)::uuid, md5('investigation')::uuid, 'monitor-pr', jsonb_build_object('force', false, 'undoOf', md5('proposal' || i)::uuid), '', 'approved',
       'succeeded', 'sidekick/' || md5('undo' || i),
       now() - make_interval(secs => i * 400), now() - make_interval(secs => i * 400) + interval '1 day', now() - make_interval(secs => i * 400),
       md5('proposal' || i)::uuid
FROM generate_series(1, 60000) AS i
WHERE i % 100 = 1 AND i % 4 = 1;

INSERT INTO pull_request_states (proposal_id, repo, number, url, state, merged_at, closed_at, checked_at)
SELECT md5('undo' || i)::uuid, 'acme/monitors', 100000 + i, 'https://github.com/acme/monitors/pull/' || (100000 + i), 'open', NULL, NULL, now() - interval '2 days'
FROM generate_series(1, 60000) AS i
WHERE i % 100 = 1 AND i % 4 = 1 AND i % 8 = 1;

INSERT INTO pull_request_states (proposal_id, repo, number, url, state, merged_at, closed_at, checked_at)
SELECT md5('proposal' || i)::uuid, 'acme/monitors', i, 'https://github.com/acme/monitors/pull/' || i,
       CASE WHEN i % 5 = 0 THEN 'open' ELSE 'merged' END,
       CASE WHEN i % 5 <> 0 THEN now() END, NULL, now() - interval '2 days'
FROM generate_series(1, 60000) AS i
WHERE i % 100 < 3 AND i % 2 = 0;

-- The CI report of each succeeded proposal, finished and sent to one output.
INSERT INTO ci_reports (proposal_id, state, detail, checks, started_at, checked_at, reported_at, seq, sent_to)
SELECT md5('proposal' || i)::uuid, 'passed', '{"state":"passed","passed":3,"total":3}', 3,
       now() - interval '2 days', now() - interval '2 days', now() - interval '2 days', i, ARRAY['slack-thread']
FROM generate_series(1, 60000) AS i
WHERE i % 100 < 3;

INSERT INTO jobs (kind, payload, status, finished_at)
SELECT 'publish', '{}', 'done', now() - make_interval(secs => i * 3)
FROM generate_series(1, 100000) AS i;

INSERT INTO jobs (kind, payload, unique_key, status, finished_at)
SELECT 'check_pull_requests', '{}', 'pr_status:' || to_char(now() - make_interval(days => i), 'YYYY-MM-DD'), 'done',
       now() - make_interval(days => i)
FROM generate_series(1, 2) AS i;

ANALYZE;
