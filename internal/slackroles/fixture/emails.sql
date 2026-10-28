-- Web UI users: Lead, an approver, and Vi, a viewer. U01KNOWN was matched
-- to Lead an hour ago, when Lead was still a responder; U01OLDMAIL was
-- matched by an email two days ago and is due a new look.
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at) VALUES
  ('01920000-0000-7000-8000-000000000050', 'https://idp.example.com', 'lead', 'lead@example.com', 'Lea', '{oncall-leads}', 'approver', now(), now()),
  ('01920000-0000-7000-8000-000000000060', 'https://idp.example.com', 'vi', 'vi@example.com', 'Vi', '{}', 'viewer', now(), now());

INSERT INTO slack_identities (slack_user_id, team_id, email, user_id, groups, role, refreshed_at) VALUES
  ('U01KNOWN', 'T01TEAM', 'lead@example.com', '01920000-0000-7000-8000-000000000050', '{}', 'responder', now() - interval '1 hour'),
  ('U01OLDMAIL', 'T01TEAM', 'lead@example.com', '01920000-0000-7000-8000-000000000050', '{}', 'approver', now() - interval '2 days');
