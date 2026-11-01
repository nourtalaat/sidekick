-- What two routes spent on 2026-11-01 before the calls of the tests: 9,000
-- micro-dollars on payments and 5,000 on search.
INSERT INTO spend_daily (day, route, connector, model, cost_micros, input_tokens, output_tokens)
VALUES
  ('2026-11-01', 'payments', 'claude-small', 'claude-small-1', 9000, 3000, 500),
  ('2026-11-01', 'search', 'claude-small', 'claude-small-1', 5000, 1500, 300);
