-- Ad is an admin. Ad made three API tokens: skt_active, a live approver
-- token; skt_revoked, revoked; and skt_expired, past its expiry.
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at) VALUES
  ('01920000-0000-7000-8000-000000000040', 'https://idp.example.com', 'ad', 'ad@example.com', 'Ad', '{sre-admins}', 'admin', now(), now());

INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at, revoked_at) VALUES
  ('01920000-0000-7000-8000-000000000041', 'ci bench', sha256(convert_to('skt_active', 'UTF8')), 'approver',
   '01920000-0000-7000-8000-000000000040', now() - interval '1 day', now() + interval '30 days', NULL),
  ('01920000-0000-7000-8000-000000000042', 'old ci', sha256(convert_to('skt_revoked', 'UTF8')), 'admin',
   '01920000-0000-7000-8000-000000000040', now() - interval '2 days', now() + interval '30 days', now() - interval '1 day'),
  ('01920000-0000-7000-8000-000000000043', 'gone', sha256(convert_to('skt_expired', 'UTF8')), 'viewer',
   '01920000-0000-7000-8000-000000000040', now() - interval '40 days', now() - interval '1 day', NULL);
