-- Users whose groups were stored before: U01FRESH an hour ago and
-- U01STALE two days ago, both in the admin group S01SRE then.
INSERT INTO slack_identities (slack_user_id, team_id, groups, role, refreshed_at)
VALUES ('U01FRESH', 'T01TEAM', '{S01SRE}', 'admin', now() - interval '1 hour'),
       ('U01STALE', 'T01TEAM', '{S01SRE}', 'admin', now() - interval '2 days');
