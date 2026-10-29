-- Fa and Fb are responders with a live session each (32 bytes of 0x06 and
-- 0x07), for the tests of feedback.
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at) VALUES
  ('01920000-0000-7000-8000-000000000060', 'https://idp.example.com', 'fa', 'fa@example.com', 'Fa', '{responders}', 'responder', now(), now()),
  ('01920000-0000-7000-8000-000000000061', 'https://idp.example.com', 'fb', 'fb@example.com', 'Fb', '{responders}', 'responder', now(), now());

INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, expires_at) VALUES
  (sha256(decode(repeat('06', 32), 'hex')), '01920000-0000-7000-8000-000000000060', 'csrf-fa', now(), now() + interval '1 hour'),
  (sha256(decode(repeat('07', 32), 'hex')), '01920000-0000-7000-8000-000000000061', 'csrf-fb', now(), now() + interval '1 hour');
