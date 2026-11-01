-- Ap is an approver with a live session (32 bytes of 0x0b) and an API token,
-- skt_approver, for the tests of action proposals. Bo is another approver
-- with a session (32 bytes of 0x0c). They decide the proposals of one
-- investigation on the payments route, in approved mode:
--   f1 and f2 wait for a decision, f3 waits but is past its expiry,
--   f4 was approved, f5 succeeded and f6 was rejected.
INSERT INTO users (id, oidc_issuer, oidc_subject, email, name, groups, role, created_at, last_login_at) VALUES
  ('01920000-0000-7000-8000-000000000070', 'https://idp.example.com', 'ap', 'ap@example.com', 'Ap', '{approvers}', 'approver', now(), now()),
  ('01920000-0000-7000-8000-000000000071', 'https://idp.example.com', 'bo', 'bo@example.com', 'Bo', '{approvers}', 'approver', now(), now());

INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, expires_at) VALUES
  (sha256(decode(repeat('0b', 32), 'hex')), '01920000-0000-7000-8000-000000000070', 'csrf-ap', now(), now() + interval '1 hour'),
  (sha256(decode(repeat('0c', 32), 'hex')), '01920000-0000-7000-8000-000000000071', 'csrf-bo', now(), now() + interval '1 hour');

INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at) VALUES
  ('01920000-0000-7000-8000-000000000072', 'deploy bot', sha256(convert_to('skt_approver', 'UTF8')), 'approver',
   '01920000-0000-7000-8000-000000000040', '2026-10-03T00:00:00Z', now() + interval '30 days');

INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
VALUES ('0192d3a4-0000-7000-8000-0000000000e1', 'fp-pay', 'payments', 'checkout-api', 'open', 1,
        '2026-10-26 14:02:00+00', '2026-10-26 14:02:00+00', '0192d3a4-0000-7000-8000-0000000000e2');

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at, alert_title)
VALUES ('0192d3a4-0000-7000-8000-0000000000e2', '0192d3a4-0000-7000-8000-0000000000e1', 'payments', 'approved', 'running',
        '{}', '{"max_tool_calls": 40, "max_wall_time_ms": 600000}', '2026-10-26 14:02:01+00', 'checkout-api p99 latency above 2s');

INSERT INTO publish_states (investigation_id, sinks, seq, phase, body, updated_at)
VALUES ('0192d3a4-0000-7000-8000-0000000000e2', '{}', 0, 'queued', '{}', '2026-10-26 14:02:01+00');

INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
                              proposed_at, expires_at, decided_by, decided_at, executed_at, result, error)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000f1', '0192d3a4-0000-7000-8000-0000000000e2', 'monitor-note',
   '{"board": "ops", "title": "Slow checkout"}', 'p99 doubled after the deploy.', 'approved', 'proposed',
   'sidekick/0192d3a4-0000-7000-8000-0000000000f1', now() - interval '3 minutes', now() + interval '23 hours', NULL, NULL, NULL, NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000f2', '0192d3a4-0000-7000-8000-0000000000e2', 'monitor-note',
   '{"board": "ops", "title": "Raise the p99 threshold", "body": "line one\nline two"}', 'The alert is noisy.', 'approved', 'proposed',
   'sidekick/0192d3a4-0000-7000-8000-0000000000f2', now() - interval '2 minutes', now() + interval '23 hours', NULL, NULL, NULL, NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000f3', '0192d3a4-0000-7000-8000-0000000000e2', 'monitor-note',
   '{"board": "ops", "title": "Old"}', 'Too late.', 'approved', 'proposed',
   'sidekick/0192d3a4-0000-7000-8000-0000000000f3', now() - interval '2 days', now() - interval '1 day', NULL, NULL, NULL, NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000f4', '0192d3a4-0000-7000-8000-0000000000e2', 'monitor-note',
   '{"board": "ops", "title": "Approved"}', 'Ok.', 'approved', 'approved',
   'sidekick/0192d3a4-0000-7000-8000-0000000000f4', now() - interval '1 hour', now() + interval '23 hours',
   '01920000-0000-7000-8000-000000000070', now() - interval '50 minutes', NULL, NULL, NULL),
  ('0192d3a4-0000-7000-8000-0000000000f5', '0192d3a4-0000-7000-8000-0000000000e2', 'monitor-note',
   '{"board": "ops", "title": "Done"}', 'Ok.', 'approved', 'succeeded',
   'sidekick/0192d3a4-0000-7000-8000-0000000000f5', now() - interval '2 hours', now() + interval '22 hours',
   '01920000-0000-7000-8000-000000000070', now() - interval '110 minutes', now() - interval '100 minutes',
   '{"summary": "Wrote a note.", "links": [{"title": "Note", "url": "https://notes.example.com/n/1"}]}', NULL),
  ('0192d3a4-0000-7000-8000-0000000000f6', '0192d3a4-0000-7000-8000-0000000000e2', 'monitor-note',
   '{"board": "ops", "title": "No"}', 'Maybe.', 'approved', 'rejected',
   'sidekick/0192d3a4-0000-7000-8000-0000000000f6', now() - interval '3 hours', now() + interval '21 hours',
   '01920000-0000-7000-8000-000000000071', now() - interval '170 minutes', NULL, NULL, NULL);
