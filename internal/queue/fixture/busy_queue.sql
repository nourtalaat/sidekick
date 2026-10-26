-- A busy queue for the query plan tests: 100,000 ready jobs of mixed kinds,
-- routes and priorities, 1,000 running jobs of which half have an expired
-- lease, 1,000 dead jobs and 20,000 done ones.

INSERT INTO jobs (kind, route, payload, priority, run_at)
SELECT (ARRAY['route_event', 'publish', 'investigate', 'watch_tick'])[1 + i % 4],
       'route-' || (i % 10),
       '{}'::jsonb,
       CASE WHEN i % 4 = 3 THEN 50 ELSE 100 END,
       now() - make_interval(secs => i)
FROM generate_series(1, 100000) AS i;

INSERT INTO jobs (kind, route, payload, status, claim_token, locked_by, locked_until)
SELECT (ARRAY['publish', 'investigate'])[1 + i % 2],
       'route-' || (i % 10),
       '{}'::jsonb,
       'running',
       nextval('job_claim_seq'),
       'fixture-worker',
       now() + make_interval(secs => i - 500)
FROM generate_series(1, 1000) AS i;

INSERT INTO jobs (kind, payload, status, attempts, last_error, finished_at)
SELECT 'publish', '{}'::jsonb, 'dead', 5, 'fixture error', now()
FROM generate_series(1, 1000) AS i;

INSERT INTO jobs (kind, payload, status, finished_at)
SELECT 'route_event', '{}'::jsonb, 'done', now()
FROM generate_series(1, 20000) AS i;

ANALYZE jobs;
