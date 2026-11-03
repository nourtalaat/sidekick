-- Six investigations for the test that the migration marks the same hours
-- as the triggers. All times are UTC unless a time zone is written.
--
--   c1 false positive at 10:37:12.345, with an API fix proposal
--   c2 false positive at 10:59:59.999, with a pull request fix proposal
--   c3 false positive at 11:00:00, with an API fix proposal
--   c4 false positive at 2026-10-22 03:30 in +05:00, which is 22:30 UTC the
--      day before, with an API fix proposal
--   c5 true positive at 15:20, with an API fix proposal
--   c6 false positive at 16:20, with a proposal that fixes no monitor
--
-- The migration marks the hours of c1 to c4 only. The triggers also mark the
-- hours of c5 and c6, because they mark every proposal.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-1', 'r1', 'closed', 1, '2026-10-20 10:30:00+00', '2026-10-20 10:30:00+00', '0192d3a4-0000-7000-8000-0000000000c1'),
  ('0192d3a4-0000-7000-8000-0000000000b2', 'fp-2', 'r1', 'closed', 1, '2026-10-20 10:30:00+00', '2026-10-20 10:30:00+00', '0192d3a4-0000-7000-8000-0000000000c2'),
  ('0192d3a4-0000-7000-8000-0000000000b3', 'fp-3', 'r1', 'closed', 1, '2026-10-20 10:30:00+00', '2026-10-20 10:30:00+00', '0192d3a4-0000-7000-8000-0000000000c3'),
  ('0192d3a4-0000-7000-8000-0000000000b4', 'fp-4', 'r1', 'closed', 1, '2026-10-20 10:30:00+00', '2026-10-20 10:30:00+00', '0192d3a4-0000-7000-8000-0000000000c4'),
  ('0192d3a4-0000-7000-8000-0000000000b5', 'fp-5', 'r1', 'closed', 1, '2026-10-20 10:30:00+00', '2026-10-20 10:30:00+00', '0192d3a4-0000-7000-8000-0000000000c5'),
  ('0192d3a4-0000-7000-8000-0000000000b6', 'fp-6', 'r1', 'closed', 1, '2026-10-20 10:30:00+00', '2026-10-20 10:30:00+00', '0192d3a4-0000-7000-8000-0000000000c6');

INSERT INTO investigations (id, group_id, route, mode, status, verdict, connectors, budget, created_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'r1', 'read-only', 'final', 'false_positive', '{}', '{}', '2026-10-20 10:37:12.345+00'),
  ('0192d3a4-0000-7000-8000-0000000000c2', '0192d3a4-0000-7000-8000-0000000000b2', 'r1', 'read-only', 'final', 'false_positive', '{}', '{}', '2026-10-20 10:59:59.999+00'),
  ('0192d3a4-0000-7000-8000-0000000000c3', '0192d3a4-0000-7000-8000-0000000000b3', 'r1', 'read-only', 'final', 'false_positive', '{}', '{}', '2026-10-20 11:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c4', '0192d3a4-0000-7000-8000-0000000000b4', 'r1', 'read-only', 'final', 'false_positive', '{}', '{}', '2026-10-22 03:30:00+05'),
  ('0192d3a4-0000-7000-8000-0000000000c5', '0192d3a4-0000-7000-8000-0000000000b5', 'r1', 'read-only', 'final', 'true_positive', '{}', '{}', '2026-10-20 15:20:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c6', '0192d3a4-0000-7000-8000-0000000000b6', 'r1', 'read-only', 'final', 'false_positive', '{}', '{}', '2026-10-20 16:20:00+00');

INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key,
                              proposed_at, expires_at, fix_kind)
VALUES
  ('0192d3a4-0000-7000-8000-000000000101', '0192d3a4-0000-7000-8000-0000000000c1', 'note', '{}', 'approved', 'succeeded', 'k-1', '2026-10-20 10:40:00+00', '2026-10-21 10:40:00+00', 'api'),
  ('0192d3a4-0000-7000-8000-000000000102', '0192d3a4-0000-7000-8000-0000000000c2', 'note', '{}', 'approved', 'succeeded', 'k-2', '2026-10-20 11:01:00+00', '2026-10-21 11:01:00+00', 'pull_request'),
  ('0192d3a4-0000-7000-8000-000000000103', '0192d3a4-0000-7000-8000-0000000000c3', 'note', '{}', 'approved', 'succeeded', 'k-3', '2026-10-20 11:05:00+00', '2026-10-21 11:05:00+00', 'api'),
  ('0192d3a4-0000-7000-8000-000000000104', '0192d3a4-0000-7000-8000-0000000000c4', 'note', '{}', 'approved', 'succeeded', 'k-4', '2026-10-21 23:00:00+00', '2026-10-22 23:00:00+00', 'api'),
  ('0192d3a4-0000-7000-8000-000000000105', '0192d3a4-0000-7000-8000-0000000000c5', 'note', '{}', 'approved', 'succeeded', 'k-5', '2026-10-20 15:30:00+00', '2026-10-21 15:30:00+00', 'api'),
  ('0192d3a4-0000-7000-8000-000000000106', '0192d3a4-0000-7000-8000-0000000000c6', 'note', '{}', 'approved', 'succeeded', 'k-6', '2026-10-20 16:30:00+00', '2026-10-21 16:30:00+00', NULL);
