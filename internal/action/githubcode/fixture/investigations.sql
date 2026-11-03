-- An investigation as the router leaves it, on a route that proposes
-- actions in approved mode, and a user who can approve.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-checkout', 'payments', 'checkout-api', 'open', 1,
   '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00', '0192d3a4-0000-7000-8000-0000000000c1');

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at, alert_title)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'payments', 'approved', 'running',
   '{}', '{"max_tool_calls": 40, "max_wall_time_ms": 600000}', '2026-10-26 14:02:01+00', 'checkout-api p99 latency above 2s');

INSERT INTO users (id, oidc_issuer, oidc_subject, name, role, created_at, last_login_at)
VALUES ('0192d3a4-0000-7000-8000-0000000000d1', 'https://idp.example.com', 'ana', 'Ana', 'approver', now(), now());
