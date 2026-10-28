-- Vi is a viewer with a live session (ID: 32 bytes of 0x01) and an
-- expired one (32 bytes of 0x02). Ad is an admin with a live session (32
-- bytes of 0x03) who made two API tokens: skt_viewer and skt_admin. Re and
-- Ro are responders with a live session each (32 bytes of 0x04 and 0x05).
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at) VALUES
  ('01920000-0000-7000-8000-000000000030', 'https://idp.example.com', 'vi', 'vi@example.com', 'Vi', '{everyone}', 'viewer', now(), now()),
  ('01920000-0000-7000-8000-000000000040', 'https://idp.example.com', 'ad', 'ad@example.com', 'Ad', '{sre-admins}', 'admin', now(), now()),
  ('01920000-0000-7000-8000-000000000050', 'https://idp.example.com', 're', 're@example.com', 'Re', '{responders}', 'responder', now(), now()),
  ('01920000-0000-7000-8000-000000000051', 'https://idp.example.com', 'ro', 'ro@example.com', 'Ro', '{responders}', 'responder', now(), now());

INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, expires_at) VALUES
  (sha256(decode(repeat('01', 32), 'hex')), '01920000-0000-7000-8000-000000000030', 'csrf-vi', now(), now() + interval '1 hour'),
  (sha256(decode(repeat('02', 32), 'hex')), '01920000-0000-7000-8000-000000000030', 'csrf-vi-old', now() - interval '13 hours', now() - interval '1 hour'),
  (sha256(decode(repeat('03', 32), 'hex')), '01920000-0000-7000-8000-000000000040', 'csrf-ad', now(), now() + interval '1 hour'),
  (sha256(decode(repeat('04', 32), 'hex')), '01920000-0000-7000-8000-000000000050', 'csrf-re', now(), now() + interval '1 hour'),
  (sha256(decode(repeat('05', 32), 'hex')), '01920000-0000-7000-8000-000000000051', 'csrf-ro', now(), now() + interval '1 hour');

INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at) VALUES
  ('01920000-0000-7000-8000-000000000041', 'dashboards', sha256(convert_to('skt_viewer', 'UTF8')), 'viewer',
   '01920000-0000-7000-8000-000000000040', '2026-10-01T00:00:00Z', now() + interval '30 days'),
  ('01920000-0000-7000-8000-000000000042', 'ops bot', sha256(convert_to('skt_admin', 'UTF8')), 'admin',
   '01920000-0000-7000-8000-000000000040', '2026-10-02T00:00:00Z', now() + interval '30 days');
