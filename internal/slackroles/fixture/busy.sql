-- 5,000 web UI users, and 5,000 Slack users with stored groups, each
-- matched to one of them, for the query plan tests.
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at)
SELECT ('01920000-0000-7000-8000-' || lpad(to_hex(i), 12, '0'))::uuid, 'https://idp.example.com', 'user-' || i,
       'user' || i || '@example.com', 'User ' || i, '{}', 'responder', now(), now()
FROM generate_series(1, 5000) AS i;

INSERT INTO slack_identities (slack_user_id, team_id, email, user_id, groups, role, refreshed_at)
SELECT 'U' || i, 'T01TEAM', 'user' || i || '@example.com', ('01920000-0000-7000-8000-' || lpad(to_hex(i), 12, '0'))::uuid,
       '{S01ONCALL}', 'responder', now()
FROM generate_series(1, 5000) AS i;

ANALYZE users;
ANALYZE slack_identities;
