-- Rollup rows of four days for the tests of the daily scores of
-- GET /stats/quality, for a server whose clock is 2026-10-28 12:00 UTC.
-- Each day has a different case: ratings on two routes, an unknown
-- confirmed verdict, a verdict that was confirmed but never given, and a
-- day of unknown ratings only.
INSERT INTO quality_daily (day, route, service, model, strategy, predicted, confirmed, n)
VALUES
  ('2026-10-20', 'r1', 'checkout', 'm1', 'default', 'false_positive', 'false_positive', 8),
  ('2026-10-20', 'r1', 'checkout', 'm1', 'default', 'false_positive', 'true_positive', 2),
  ('2026-10-20', 'r2', 'payments', 'm2', 'deep', 'true_positive', 'true_positive', 3),
  ('2026-10-21', 'r1', 'checkout', 'm1', 'default', 'false_positive', 'false_positive', 4),
  ('2026-10-21', 'r1', 'checkout', 'm1', 'default', 'false_positive', 'unknown', 2),
  ('2026-10-21', 'r2', 'payments', 'm2', 'deep', 'true_positive', 'true_positive', 1),
  ('2026-10-22', 'r1', 'checkout', 'm1', 'default', 'true_positive', 'duplicate', 3),
  ('2026-10-22', 'r1', 'checkout', 'm1', 'default', 'true_positive', 'true_positive', 1),
  ('2026-10-23', 'r2', 'payments', 'm2', 'deep', 'false_positive', 'unknown', 2);
