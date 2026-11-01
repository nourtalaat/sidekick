-- A busy database for the query plan tests: 5,000 investigations with ten
-- proposals each, one in a hundred still proposed, 1,000 users and 100 API
-- tokens.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, window_until, closed_reason)
SELECT md5('group' || i)::uuid, 'fp-' || i, 'default', 'closed', 1,
       now() - make_interval(secs => i * 60), now() - make_interval(secs => i * 60),
       now() - make_interval(secs => i * 60) + interval '30 minutes', 'window_ended'
FROM generate_series(1, 5000) AS i;

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, run, created_at, alert_title)
SELECT md5('investigation' || i)::uuid, md5('group' || i)::uuid, 'default', 'approved', 'final', '{}', '{}', 1,
       now() - make_interval(secs => i * 60), 'Alert ' || i
FROM generate_series(1, 5000) AS i;

INSERT INTO users (id, oidc_issuer, oidc_subject, name, role, created_at, last_login_at)
SELECT md5('user' || i)::uuid, 'https://idp.example.com', 'user-' || i, 'User ' || i, 'approver', now(), now()
FROM generate_series(1, 1000) AS i;

INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at)
SELECT md5('token' || i)::uuid, 'token ' || i, sha256(convert_to('skt_' || i, 'UTF8')), 'approver', md5('user1')::uuid, now(), now() + interval '30 days'
FROM generate_series(1, 100) AS i;

INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
                              proposed_at, expires_at, decided_by, decided_at, executed_at)
SELECT md5('proposal' || i || '-' || n)::uuid, md5('investigation' || i)::uuid, 'monitor-note', '{"title": "Note"}', 'Because.', 'approved',
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
FROM generate_series(1, 5000) AS i, generate_series(1, 10) AS n;

ANALYZE;
