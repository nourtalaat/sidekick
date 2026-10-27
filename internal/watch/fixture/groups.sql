-- For the watcher tests: an open group of the default route with its
-- investigation and one firing alert, and a second open group without
-- events.
INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, window_until, investigation_id)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000b1', 'fp-checkout', 'default', 'checkout', 'open', 1,
   '2026-10-26 09:00:00+00', '2026-10-26 09:00:00+00', '2026-10-26 09:30:00+00', '0192d3a4-0000-7000-8000-0000000000c1'),
  ('0192d3a4-0000-7000-8000-0000000000b2', 'fp-other', 'default', 'other', 'open', 1,
   '2026-10-26 09:00:00+00', '2026-10-26 09:00:00+00', '2026-10-26 09:30:00+00', NULL);

INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000c1', '0192d3a4-0000-7000-8000-0000000000b1', 'default', 'read-only', 'running',
   '{}', '{}', '2026-10-26 09:00:01+00');

INSERT INTO events (id, intake, key, kind, trusted, alert_name, service, labels, fingerprint, status, route, group_id, received_at)
VALUES
  ('0192d3a4-0000-7000-8000-000000000001', 'alertmanager', 'checkout-1', 'alert', true, 'CheckoutDown', 'checkout',
   '{}', 'alert-checkout', 'grouped', 'default', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 09:00:00+00');
