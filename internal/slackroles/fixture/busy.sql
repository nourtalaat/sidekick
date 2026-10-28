-- 5,000 Slack users with stored groups, for the query plan tests.
INSERT INTO slack_identities (slack_user_id, team_id, groups, role, refreshed_at)
SELECT 'U' || i, 'T01TEAM', '{S01ONCALL}', 'responder', now()
FROM generate_series(1, 5000) AS i;

ANALYZE slack_identities;
