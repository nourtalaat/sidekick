-- Re is a responder with a live session (32 bytes of 0x04) and an API
-- token, skt_responder. Together with fixture/rules.sql the runtime rules
-- are: the listener ops, the listener alerts (the config file has one of
-- that name, so it is shadowed), the routes checkout (turned off), payments
-- (shadowed by the file route) and broken (names a source the file does not
-- have, so it is invalid).
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at) VALUES
  ('01920000-0000-7000-8000-000000000050', 'https://idp.example.com', 're', 're@example.com', 'Re', '{responders}', 'responder', now(), now());

INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, expires_at) VALUES
  (sha256(decode(repeat('04', 32), 'hex')), '01920000-0000-7000-8000-000000000050', 'csrf-re', now(), now() + interval '1 hour');

INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at) VALUES
  ('01920000-0000-7000-8000-000000000051', 'responder bot', sha256(convert_to('skt_responder', 'UTF8')), 'responder',
   '01920000-0000-7000-8000-000000000040', '2026-10-03T00:00:00Z', now() + interval '30 days');

INSERT INTO listeners (name, slack_channel, match, trusted, enabled, version, updated_by, updated_at)
VALUES ('alerts', '#old-alerts', '{}', false, true, 1, 'U01SEED', '2026-10-20T10:00:00Z');

INSERT INTO routes (name, priority, spec, enabled, version, updated_by, updated_at) VALUES
  ('payments', 10, '{"match": {"service": "pay-*"}, "mode": "read-only"}', true, 1, 'U01SEED', '2026-10-20T10:00:00Z'),
  ('broken', 30, '{"match": {"service": "broken-*"}, "mode": "read-only", "sources": ["gone"]}', true, 1, 'U01SEED', '2026-10-20T10:00:00Z');
