-- 5,000 users, each with a live session, and 500 API tokens, for the
-- query plan tests.
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at)
SELECT ('01920000-0000-7000-8000-' || lpad(to_hex(i), 12, '0'))::uuid, 'https://idp.example.com', 'user-' || i,
       'user' || i || '@example.com', 'User ' || i, '{engineering}', 'responder', now(), now()
FROM generate_series(1, 5000) AS i;

INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, expires_at)
SELECT sha256(int4send(i)), ('01920000-0000-7000-8000-' || lpad(to_hex(i), 12, '0'))::uuid, 'csrf-' || i,
       now(), now() + interval '6 hours'
FROM generate_series(1, 5000) AS i;

INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at)
SELECT ('01920000-0000-7000-9000-' || lpad(to_hex(i), 12, '0'))::uuid, 'token ' || i, sha256(int4send(-i)), 'viewer',
       ('01920000-0000-7000-8000-' || lpad(to_hex(i), 12, '0'))::uuid, now(), now() + interval '30 days'
FROM generate_series(1, 500) AS i;

ANALYZE users;
ANALYZE sessions;
ANALYZE api_tokens;
