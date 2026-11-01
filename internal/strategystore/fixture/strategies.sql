-- Runtime strategies that exist before each test: checkout-latency with two
-- versions, saved by the user Sam.
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at)
VALUES ('01920000-0000-7000-8000-000000000070', 'https://idp.example.com', 'sam', 'sam@example.com', 'Sam', '{sre-admins}', 'admin', now(), now());

INSERT INTO strategies (name, current_version, created_at)
VALUES ('checkout-latency', 2, '2026-10-20T10:00:00Z');

INSERT INTO strategy_versions (name, version, body, author, reason, created_at) VALUES
  ('checkout-latency', 1, $body$name: checkout-latency
description: Checkout is slow.
$body$, '01920000-0000-7000-8000-000000000070', 'First draft', '2026-10-20T10:00:00Z'),
  ('checkout-latency', 2, $body$name: checkout-latency
description: Checkout is slow.
first:
  - Check what changed.
$body$, '01920000-0000-7000-8000-000000000070', 'Add the first check', '2026-10-21T10:00:00Z');
