-- Runtime strategies for the strategy API tests. Ad, the admin of
-- fixture/sessions.sql, saved them.
--
--   checkout-latency  two versions, in effect
--   file-latency      one version; fixture/strategies/file-latency.yaml has
--                     the same name, so it is shadowed
--   broken            one version that fails the strategy checks, so it is
--                     invalid
INSERT INTO strategies (name, current_version, created_at) VALUES
  ('checkout-latency', 2, '2026-10-20T10:00:00Z'),
  ('file-latency', 1, '2026-10-20T10:00:00Z'),
  ('broken', 1, '2026-10-20T10:00:00Z');

INSERT INTO strategy_versions (name, version, body, author, reason, created_at) VALUES
  ('checkout-latency', 1, $body$name: checkout-latency
description: Checkout is slow.
$body$, '01920000-0000-7000-8000-000000000040', 'First draft', '2026-10-20T10:00:00Z'),
  ('checkout-latency', 2, $body$name: checkout-latency
description: Checkout is slow.
first:
  - Check what changed.
$body$, '01920000-0000-7000-8000-000000000040', 'Add the first check', '2026-10-21T10:00:00Z'),
  ('file-latency', 1, $body$name: file-latency
description: Saved before the file existed.
$body$, '01920000-0000-7000-8000-000000000040', 'Early version', '2026-10-20T10:00:00Z'),
  ('broken', 1, $body$name: broken
$body$, '01920000-0000-7000-8000-000000000040', 'Saved by an older release', '2026-10-20T10:00:00Z');
