-- More proposals on the investigation of proposals.sql, for the tests of the
-- action filter: three of ticket-comment and one of slack-react, all
-- decided before f6, newest first (a1 is the newest).
--   a1 ticket-comment succeeded, a2 ticket-comment failed,
--   a3 ticket-comment succeeded, a4 slack-react failed.
INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
                              proposed_at, expires_at, executed_at, error)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', '0192d3a4-0000-7000-8000-0000000000e2', 'ticket-comment',
   '{"ticket": "OPS-1"}', 'Ok.', 'approved', 'succeeded',
   'sidekick/0192d3a4-0000-7000-8000-0000000000a1', now() - interval '4 hours', now() + interval '20 hours', now() - interval '230 minutes', NULL),
  ('0192d3a4-0000-7000-8000-0000000000a2', '0192d3a4-0000-7000-8000-0000000000e2', 'ticket-comment',
   '{"ticket": "OPS-2"}', 'Ok.', 'approved', 'failed',
   'sidekick/0192d3a4-0000-7000-8000-0000000000a2', now() - interval '5 hours', now() + interval '19 hours', NULL, 'The ticket is closed.'),
  ('0192d3a4-0000-7000-8000-0000000000a3', '0192d3a4-0000-7000-8000-0000000000e2', 'ticket-comment',
   '{"ticket": "OPS-3"}', 'Ok.', 'approved', 'succeeded',
   'sidekick/0192d3a4-0000-7000-8000-0000000000a3', now() - interval '6 hours', now() + interval '18 hours', now() - interval '350 minutes', NULL),
  ('0192d3a4-0000-7000-8000-0000000000a4', '0192d3a4-0000-7000-8000-0000000000e2', 'slack-react',
   '{"emoji": "eyes"}', 'Ok.', 'approved', 'failed',
   'sidekick/0192d3a4-0000-7000-8000-0000000000a4', now() - interval '7 hours', now() + interval '17 hours', NULL, 'The channel is archived.');
