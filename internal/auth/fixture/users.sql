-- Old Ana signed in through another provider with the email the fake
-- provider gives its default user, in other case. Bo is a responder with
-- a live session (ID: 32 bytes of 0x01) and an expired one (32 bytes of
-- 0x02).
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at) VALUES
  ('01920000-0000-7000-8000-000000000010', 'https://old.example.com', 'old-ana', 'ANA@example.com', 'Old Ana', '{}', 'viewer', now(), now()),
  ('01920000-0000-7000-8000-000000000020', 'https://idp.example.com', 'bo', 'bo@example.com', 'Bo', '{engineering}', 'responder', now(), now());

INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, expires_at) VALUES
  (sha256(decode(repeat('01', 32), 'hex')), '01920000-0000-7000-8000-000000000020', 'csrf-live', now(), now() + interval '1 hour'),
  (sha256(decode(repeat('02', 32), 'hex')), '01920000-0000-7000-8000-000000000020', 'csrf-expired', now() - interval '13 hours', now() - interval '1 hour');
