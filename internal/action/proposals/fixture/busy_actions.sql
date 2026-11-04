-- More of the busy database of busy.sql, for the plans of the action
-- filter: 5,000 older investigations with ten proposals each, which brings
-- the table to 100,000 proposals. The proposals here are spread over seven
-- actions, one of them rare (about one in 500), in every status. busy.sql
-- must be loaded first.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, window_until, closed_reason)
SELECT md5('group' || i)::uuid, 'fp-' || i, 'default', 'closed', 1,
       now() - make_interval(secs => i * 60), now() - make_interval(secs => i * 60),
       now() - make_interval(secs => i * 60) + interval '30 minutes', 'window_ended'
FROM generate_series(5001, 10000) AS i;

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, run, created_at, alert_title)
SELECT md5('investigation' || i)::uuid, md5('group' || i)::uuid, 'default', 'approved', 'final', '{}', '{}', 1,
       now() - make_interval(secs => i * 60), 'Alert ' || i
FROM generate_series(5001, 10000) AS i;

INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
                              proposed_at, expires_at, decided_by, decided_at, executed_at)
SELECT md5('proposal' || i || '-' || n)::uuid, md5('investigation' || i)::uuid,
       CASE WHEN h < 500 THEN 'monitor-note'
            WHEN h < 700 THEN 'slack-react'
            WHEN h < 850 THEN 'ticket-comment'
            WHEN h < 950 THEN 'monitor-pr'
            WHEN h < 990 THEN 'slack-post'
            WHEN h < 998 THEN 'monitor-mute'
            ELSE 'monitor-rollback' END,
       '{"title": "Note"}', 'Because.', 'approved',
       CASE WHEN (i * 10 + n) % 100 = 0 THEN 'proposed'
            WHEN n % 4 = 0 THEN 'rejected'
            WHEN n % 4 = 1 THEN 'expired'
            WHEN n % 4 = 2 THEN 'succeeded'
            ELSE 'failed' END,
       'sidekick/' || md5('proposal' || i || '-' || n),
       now() - make_interval(secs => i * 60 - n), now() - make_interval(secs => i * 60 - n) + interval '24 hours',
       CASE WHEN (i * 10 + n) % 100 = 0 OR n % 4 = 1 THEN NULL ELSE md5('user' || (1 + i % 1000))::uuid END,
       CASE WHEN (i * 10 + n) % 100 = 0 OR n % 4 = 1 THEN NULL ELSE now() - make_interval(secs => i * 60 - n - 5) END,
       CASE WHEN n % 4 IN (2, 3) THEN now() - make_interval(secs => i * 60 - n - 10) ELSE NULL END
FROM (SELECT i, n, abs(hashtext(i || '-' || n)) % 1000 AS h
      FROM generate_series(5001, 10000) AS i, generate_series(1, 10) AS n) AS s;

-- Pull requests for the succeeded proposals n = 2, 6 and 10 of every
-- investigation, 30,000 in all with those of busy.sql. A table this size
-- makes a sequential scan of it dearer than an index lookup for each of the
-- few dozen proposals an action filter finds, whatever the estimate of that
-- number is.
INSERT INTO pull_request_states (proposal_id, repo, number, url, state, merged_at, closed_at, checked_at)
SELECT md5('proposal' || i || '-' || n)::uuid, 'acme/monitors', i, 'https://github.com/acme/monitors/pull/' || i,
       CASE i % 3 WHEN 0 THEN 'open' WHEN 1 THEN 'merged' ELSE 'closed' END,
       CASE WHEN i % 3 = 1 THEN now() - make_interval(secs => i * 60 - 20) END,
       CASE WHEN i % 3 = 2 THEN now() - make_interval(secs => i * 60 - 20) END,
       now() - make_interval(secs => i * 60 - 30)
FROM generate_series(1, 10000) AS i, (VALUES (2), (6), (10)) AS v(n)
WHERE i > 5000 OR n > 2;

-- A CI report for each of those pull requests, so the join with ci_reports
-- of the proposal queries reads a table of the same size.
INSERT INTO ci_reports (proposal_id, state, detail, checks, started_at, checked_at, reported_at)
SELECT proposal_id, 'passed', '{"state":"passed","passed":3,"total":3}', 3, checked_at, checked_at, checked_at
FROM pull_request_states;

-- ANALYZE samples 30,000 rows by default, so the estimate of how many rows
-- a rare action has changed from run to run. A statistics target of 1,000
-- makes it read every row of the table, and the estimate the same each time.
ALTER TABLE action_proposals ALTER COLUMN action SET STATISTICS 1000;

ANALYZE;
