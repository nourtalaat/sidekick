-- Event groups as they were before the unique index on open groups: every
-- routed event opened its own group, so two open groups share fp-disk.
INSERT INTO event_groups (id, fingerprint, route, status, event_count, opened_at, last_event_at, closed_reason)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000a1', 'fp-disk', 'default', 'open', 1,
   '2026-10-26 10:00:00+00', '2026-10-26 10:00:00+00', NULL),
  ('0192d3a4-0000-7000-8000-0000000000a2', 'fp-disk', 'default', 'open', 1,
   '2026-10-26 10:05:00+00', '2026-10-26 10:05:00+00', NULL),
  ('0192d3a4-0000-7000-8000-0000000000a3', 'fp-disk', 'default', 'closed', 1,
   '2026-10-26 09:00:00+00', '2026-10-26 09:00:00+00', 'window_ended'),
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-cpu', 'default', 'open', 1,
   '2026-10-26 09:30:00+00', '2026-10-26 09:30:00+00', NULL);
