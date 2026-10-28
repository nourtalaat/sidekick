-- Three dead jobs (1, 2 and 3, which died last), a pending one (4), and
-- a dead job (5) whose unique key a pending job (6) holds, so it cannot be
-- retried. Job 1's last error holds an email, which the list redacts.
INSERT INTO jobs (id, kind, route, payload, unique_key, status, attempts, max_attempts, last_error, created_at, finished_at) VALUES
  (1, 'publish', 'default', '{}', NULL, 'dead', 5, 5, 'slack: not allowed to post as ana@example.com', '2026-10-27T08:00:00Z', '2026-10-27T09:00:00Z'),
  (2, 'investigate', 'checkout', '{}', NULL, 'dead', 5, 5, 'model: overloaded', '2026-10-27T08:00:00Z', '2026-10-27T10:00:00Z'),
  (3, 'investigate', NULL, '{}', NULL, 'dead', 1, 5, 'decode payload: unexpected end of JSON input', '2026-10-27T08:00:00Z', '2026-10-27T11:00:00Z'),
  (4, 'publish', 'default', '{}', NULL, 'pending', 0, 5, NULL, '2026-10-27T08:00:00Z', NULL),
  (5, 'route_event', NULL, '{}', 'event-1', 'dead', 5, 5, 'boom', '2026-10-27T07:00:00Z', '2026-10-27T07:30:00Z'),
  (6, 'route_event', NULL, '{}', 'event-1', 'pending', 0, 5, NULL, '2026-10-27T08:00:00Z', NULL);

SELECT setval('jobs_id_seq', 100);
