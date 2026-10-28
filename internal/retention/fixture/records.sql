-- Records inside and outside the test periods, for a run at
-- 2026-10-28 12:00 UTC with events 10d, investigations 30d, steps 5d,
-- audit log 20d and watcher checks 3d. Each table has at least two old
-- rows, so a batch of one shows which goes first.
--
-- Groups and investigations: c1 (40 days old) and c2 (35 days) are old
-- and closed; c3 (41 days) is old but its group is still open; c4
-- (8 days) is new.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, closed_reason, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-1', 'default', 'closed', 1, '2026-09-18 12:00:00+00', '2026-09-18 12:00:00+00', 'resolved', '0192d3a4-0000-7000-8000-0000000000c1'),
  ('0192d3a4-0000-7000-8000-0000000000b2', 'fp-2', 'default', 'closed', 1, '2026-09-23 12:00:00+00', '2026-09-23 12:00:00+00', 'window_ended', '0192d3a4-0000-7000-8000-0000000000c2'),
  ('0192d3a4-0000-7000-8000-0000000000b3', 'fp-3', 'default', 'open', 1, '2026-09-17 12:00:00+00', '2026-10-28 11:00:00+00', NULL, '0192d3a4-0000-7000-8000-0000000000c3'),
  ('0192d3a4-0000-7000-8000-0000000000b4', 'fp-4', 'default', 'closed', 1, '2026-10-20 12:00:00+00', '2026-10-20 12:00:00+00', 'resolved', '0192d3a4-0000-7000-8000-0000000000c4');

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'default', 'read-only', 'final', '{}', '{}', '2026-09-18 12:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c2', '0192d3a4-0000-7000-8000-0000000000b2', 'default', 'read-only', 'final', '{}', '{}', '2026-09-23 12:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c3', '0192d3a4-0000-7000-8000-0000000000b3', 'default', 'read-only', 'final', '{}', '{}', '2026-09-17 12:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c4', '0192d3a4-0000-7000-8000-0000000000b4', 'default', 'read-only', 'final', '{}', '{}', '2026-10-20 12:00:00+00');

-- Steps: c3's step is the oldest, c4 has one old step and one new.
INSERT INTO steps (investigation_id, seq, run, kind, parts, status, started_at, duration_ms)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', 0, 1, 'prompt', '[]', 'ok', '2026-09-18 12:00:00+00', 1),
  ('0192d3a4-0000-7000-8000-0000000000c1', 1, 1, 'model_turn', '[]', 'ok', '2026-09-18 12:01:00+00', 1),
  ('0192d3a4-0000-7000-8000-0000000000c2', 0, 1, 'prompt', '[]', 'ok', '2026-09-23 12:00:00+00', 1),
  ('0192d3a4-0000-7000-8000-0000000000c3', 0, 1, 'prompt', '[]', 'ok', '2026-09-17 12:00:00+00', 1),
  ('0192d3a4-0000-7000-8000-0000000000c4', 0, 1, 'prompt', '[]', 'ok', '2026-10-20 12:00:00+00', 1),
  ('0192d3a4-0000-7000-8000-0000000000c4', 1, 1, 'model_turn', '[]', 'ok', '2026-10-27 12:00:00+00', 1);

-- What belongs to c1 and c4 besides steps.
INSERT INTO investigation_inbox (investigation_id, kind, body, created_at, delivered_seq)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', 'new_event', '{}', '2026-09-18 12:00:30+00', 1),
  ('0192d3a4-0000-7000-8000-0000000000c4', 'new_event', '{}', '2026-10-20 12:00:30+00', NULL);

INSERT INTO output_refs (investigation_id, sink, external, ref, last_seq, published_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', 'slack-thread', 'C1:1.1', '{}', 1, '2026-09-18 12:02:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c4', 'slack-thread', 'C1:4.4', '{}', 1, '2026-10-20 12:02:00+00');

INSERT INTO publish_states (investigation_id, sinks, seq, phase, body, updated_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '{slack-thread}', 1, 'final', '{}', '2026-09-18 12:02:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c4', '{slack-thread}', 1, 'final', '{}', '2026-10-20 12:02:00+00');

INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, slack_user_id, channel, created_at, updated_at)
VALUES ('0192d3a4-0000-7000-8000-0000000000e1', '0192d3a4-0000-7000-8000-0000000000c1', 'verdict', 'run:1/verdict', 'correct',
        'U01ANA', 'slack', '2026-09-18 13:00:00+00', '2026-09-18 13:00:00+00');

-- Watchers: d1 of c1, d2 a route watcher of c1's group with no
-- investigation, d4 of c4.
INSERT INTO watchers (id, group_id, investigation_id, name, origin, check_type, spec, interval_s, passes_to_recover,
                      check_timeout_s, status, started_at, deadline)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000d1', '0192d3a4-0000-7000-8000-0000000000b1', '0192d3a4-0000-7000-8000-0000000000c1',
   'health', 'model', 'alert', '{}', 30, 2, 10, 'recovered', '2026-09-18 12:05:00+00', '2026-09-18 13:05:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000d2', '0192d3a4-0000-7000-8000-0000000000b1', NULL,
   'route-health', 'route', 'alert', '{}', 30, 2, 10, 'recovered', '2026-09-18 12:00:00+00', '2026-09-18 13:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000d4', '0192d3a4-0000-7000-8000-0000000000b4', '0192d3a4-0000-7000-8000-0000000000c4',
   'health', 'model', 'alert', '{}', 30, 2, 10, 'running', '2026-10-24 12:00:00+00', '2026-10-29 12:00:00+00');

INSERT INTO watcher_changes (investigation_id, seq, state, created_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', 2, '{}', '2026-09-18 12:05:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000c4', 2, '{}', '2026-10-24 12:00:00+00');

-- Watcher checks: d1's is the oldest; d4 has two old checks and one new.
INSERT INTO watcher_checks (watcher_id, seq, at, result, duration_ms)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000d1', 1, '2026-09-19 12:00:00+00', 'pass', 1),
  ('0192d3a4-0000-7000-8000-0000000000d4', 1, '2026-10-24 12:00:00+00', 'fail', 1),
  ('0192d3a4-0000-7000-8000-0000000000d4', 2, '2026-10-25 00:00:00+00', 'fail', 1),
  ('0192d3a4-0000-7000-8000-0000000000d4', 3, '2026-10-27 12:00:00+00', 'pass', 1);

-- Events: ev-4 (no group) is the oldest, ev-1 and ev-2 are old, ev-3 is
-- new.
INSERT INTO events (id, intake, key, kind, trusted, fingerprint, status, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'grafana', 'ev-1', 'alert', false, 'fp-1', 'grouped', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-16 12:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a2', 'grafana', 'ev-2', 'alert', false, 'fp-2', 'grouped', '0192d3a4-0000-7000-8000-0000000000b2', '2026-10-17 12:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a3', 'grafana', 'ev-3', 'alert', false, 'fp-4', 'grouped', '0192d3a4-0000-7000-8000-0000000000b4', '2026-10-26 12:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000a4', 'grafana', 'ev-4', 'alert', false, 'fp-9', 'skipped', NULL, '2026-10-13 12:00:00+00');

-- Audit log: audit-1 and audit-2 are old, audit-3 is new.
INSERT INTO audit_log (at, actor_kind, action)
VALUES
  ('2026-10-03 12:00:00+00', 'system', 'audit-1'),
  ('2026-10-07 12:00:00+00', 'system', 'audit-2'),
  ('2026-10-23 12:00:00+00', 'system', 'audit-3');

-- Memory entries: e1 and the disabled e2 are old; the newer one belongs to
-- c1, an old investigation, and outlives it.
INSERT INTO memory_entries (investigation_id, fingerprint, verdict, summary, confirmed_at, disabled)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000e1', 'fp-1', 'true_positive', 'Old.', '2026-10-03 12:00:00+00', false),
  ('0192d3a4-0000-7000-8000-0000000000e2', 'fp-2', 'false_positive', 'Old and disabled.', '2026-10-07 12:00:00+00', true),
  ('0192d3a4-0000-7000-8000-0000000000c1', 'fp-1', 'true_positive', 'New.', '2026-10-23 12:00:00+00', false);

-- Jobs: done jobs are kept 1 day and dead jobs 30 days.
INSERT INTO jobs (kind, payload, unique_key, status, finished_at)
VALUES
  ('publish', '{}', 'job-done-3d', 'done', '2026-10-25 12:00:00+00'),
  ('publish', '{}', 'job-done-2d', 'done', '2026-10-26 12:00:00+00'),
  ('publish', '{}', 'job-done-1h', 'done', '2026-10-28 11:00:00+00'),
  ('publish', '{}', 'job-dead-31d', 'dead', '2026-09-27 12:00:00+00'),
  ('publish', '{}', 'job-dead-2d', 'dead', '2026-10-26 12:00:00+00');

INSERT INTO jobs (kind, payload, unique_key, created_at)
VALUES ('publish', '{}', 'job-pending', '2026-09-01 12:00:00+00');
