-- Two investigations as the router leaves them, for the proposal tests:
-- checkout, whose route proposes actions in approved mode, and disk, which
-- is read-only. Also a user and an API token that can decide.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-checkout', 'payments', 'checkout-api', 'open', 1,
   '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00', '0192d3a4-0000-7000-8000-0000000000c1'),
  ('0192d3a4-0000-7000-8000-0000000000b2', 'fp-disk', 'default', 'db', 'open', 1,
   '2026-10-26 14:05:00+00', '2026-10-26 14:05:00+00', '0192d3a4-0000-7000-8000-0000000000c2');

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at, alert_title)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'payments', 'approved', 'running',
   '{}', '{"max_tool_calls": 40, "max_wall_time_ms": 600000}', '2026-10-26 14:02:01+00', 'checkout-api p99 latency above 2s'),
  ('0192d3a4-0000-7000-8000-0000000000c2', '0192d3a4-0000-7000-8000-0000000000b2', 'default', 'read-only', 'running',
   '{}', '{"max_tool_calls": 40, "max_wall_time_ms": 600000}', '2026-10-26 14:05:01+00', 'Disk almost full');

INSERT INTO users (id, oidc_issuer, oidc_subject, name, role, created_at, last_login_at)
VALUES ('0192d3a4-0000-7000-8000-0000000000d1', 'https://idp.example.com', 'ana', 'Ana', 'approver', now(), now());

INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at)
VALUES ('0192d3a4-0000-7000-8000-0000000000d2', 'ci-approver', sha256(convert_to('skt_ci', 'UTF8')), 'approver',
        '0192d3a4-0000-7000-8000-0000000000d1', now(), now() + interval '30 days');
