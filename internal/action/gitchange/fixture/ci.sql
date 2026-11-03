-- One investigation with two succeeded proposals that opened pull requests:
-- the first (c1000000-...-01) by the action fix-code, and the second
-- (c1000000-...-02) by an action that reports no CI. For the CI report tests.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, closed_reason, investigation_id)
VALUES ('a1000000-0000-4000-8000-000000000001', 'fp', 'payments', 'closed', 1, now(), now(), 'window_ended', 'a2000000-0000-4000-8000-000000000001');

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
VALUES ('a2000000-0000-4000-8000-000000000001', 'a1000000-0000-4000-8000-000000000001', 'payments', 'approved', 'final', '{}', '{}', now());

INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key, proposed_at, expires_at, executed_at, result)
VALUES
  ('c1000000-0000-4000-8000-000000000001', 'a2000000-0000-4000-8000-000000000001', 'fix-code', '{"repo":"acme/monitors"}', '', 'approved', 'succeeded',
   'sidekick/c1000000-0000-4000-8000-000000000001', now(), now() + interval '1 day', now(),
   '{"summary":"Opened a pull request.","links":[],"pullRequest":{"number":12,"headSHA":"4f2c1a9"}}'),
  ('c1000000-0000-4000-8000-000000000002', 'a2000000-0000-4000-8000-000000000001', 'other', '{}', '', 'approved', 'succeeded',
   'sidekick/c1000000-0000-4000-8000-000000000002', now(), now() + interval '1 day', now(),
   '{"summary":"Opened a pull request.","links":[],"pullRequest":{"number":13,"headSHA":"9c2d7e1"}}');
