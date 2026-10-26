-- name: EnqueueJob :one
-- EnqueueJob inserts a pending job and returns its ID. When unique_key
-- matches a pending or running job, it inserts nothing and returns no row.
INSERT INTO jobs (kind, route, payload, unique_key, priority, run_at, max_attempts, traceparent)
VALUES (
  @kind,
  sqlc.narg(route),
  @payload,
  sqlc.narg(unique_key),
  @priority,
  COALESCE(sqlc.narg(run_at)::timestamptz, now()),
  @max_attempts,
  sqlc.narg(traceparent)
)
ON CONFLICT (unique_key) WHERE status IN ('pending', 'running') DO NOTHING
RETURNING id;

-- name: NotifyJobsReady :exec
-- NotifyJobsReady wakes the workers listening on jobs_ready. Inside a
-- transaction, Postgres sends the notification when it commits.
SELECT pg_notify('jobs_ready', @kind::text);

-- name: ListenJobsReady :exec
LISTEN jobs_ready;

-- name: ClaimJobs :many
-- ClaimJobs leases up to batch ready jobs of the given kinds to worker, each
-- with a new claim token. It does not change attempts.
UPDATE jobs
SET status = 'running',
    claim_token = nextval('job_claim_seq'),
    locked_by = @worker::text,
    locked_until = now() + make_interval(secs => @lease_seconds::float8)
WHERE id IN (
  SELECT id FROM jobs
  WHERE status = 'pending' AND run_at <= now() AND kind = ANY (@kinds::text[])
  ORDER BY priority, run_at
  LIMIT @batch
  FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: LockLimitedClaims :exec
-- LockLimitedClaims takes the transaction-level advisory lock that makes the
-- running counts in ClaimLimitedJob exact across replicas.
SELECT pg_advisory_xact_lock(@lock_key::bigint);

-- name: ClaimLimitedJob :one
-- ClaimLimitedJob leases the oldest ready job of kind whose route is under
-- its limit, when fewer than global_limit jobs of limited_kinds are running.
-- Routes not in routes have no limit of their own. Run it after
-- LockLimitedClaims, in the same transaction.
WITH running AS (
  SELECT route FROM jobs
  WHERE status = 'running' AND kind = ANY (@limited_kinds::text[])
),
route_limits AS (
  SELECT unnest(@routes::text[]) AS route, unnest(@route_limits::int[]) AS max_running
),
candidate AS (
  SELECT j.id FROM jobs j
  LEFT JOIN route_limits l ON l.route = j.route
  WHERE j.status = 'pending' AND j.run_at <= now() AND j.kind = @kind
    AND (SELECT count(*) FROM running) < @global_limit::int
    AND (l.max_running IS NULL OR (SELECT count(*) FROM running r WHERE r.route = j.route) < l.max_running)
  ORDER BY j.priority, j.run_at
  LIMIT 1
  FOR UPDATE OF j SKIP LOCKED
)
UPDATE jobs
SET status = 'running',
    claim_token = nextval('job_claim_seq'),
    locked_by = @worker::text,
    locked_until = now() + make_interval(secs => @lease_seconds::float8)
WHERE id = (SELECT id FROM candidate)
RETURNING *;

-- name: RenewLease :execrows
-- RenewLease extends the lease of a running job that the token still owns.
UPDATE jobs
SET locked_until = now() + make_interval(secs => @lease_seconds::float8)
WHERE id = @id AND claim_token = @claim_token::bigint AND status = 'running';

-- name: CompleteJob :execrows
UPDATE jobs
SET status = 'done', finished_at = now(), locked_by = NULL, locked_until = NULL
WHERE id = @id AND claim_token = @claim_token::bigint AND status = 'running';

-- name: FailJob :one
-- FailJob counts a failed attempt. The job goes back to pending after
-- backoff_seconds, or becomes dead when give_up is set or it has used all of
-- its attempts. It returns the updated job.
UPDATE jobs
SET attempts = attempts + 1,
    last_error = @last_error::text,
    status = CASE WHEN @give_up::boolean OR attempts + 1 >= max_attempts THEN 'dead' ELSE 'pending' END,
    run_at = CASE WHEN @give_up::boolean OR attempts + 1 >= max_attempts THEN run_at
                  ELSE now() + make_interval(secs => @backoff_seconds::float8) END,
    finished_at = CASE WHEN @give_up::boolean OR attempts + 1 >= max_attempts THEN now() END,
    locked_by = NULL,
    locked_until = NULL
WHERE id = @id AND claim_token = @claim_token::bigint AND status = 'running'
RETURNING *;

-- name: HandBackJob :execrows
-- HandBackJob returns a running job to pending at once, with attempts
-- unchanged, for a worker that is shutting down.
UPDATE jobs
SET status = 'pending', run_at = now(), locked_by = NULL, locked_until = NULL
WHERE id = @id AND claim_token = @claim_token::bigint AND status = 'running';

-- name: TryLockReaper :one
SELECT pg_try_advisory_lock(@lock_key::bigint) AS locked;

-- name: UnlockReaper :one
SELECT pg_advisory_unlock(@lock_key::bigint) AS unlocked;

-- name: ReapExpiredLeases :many
-- ReapExpiredLeases counts an attempt for every running job whose lease
-- expired, and sets it back to pending, or to dead when it has used all of
-- its attempts. It returns the updated jobs.
UPDATE jobs
SET attempts = attempts + 1,
    last_error = 'lease expired',
    status = CASE WHEN attempts + 1 >= max_attempts THEN 'dead' ELSE 'pending' END,
    run_at = now(),
    finished_at = CASE WHEN attempts + 1 >= max_attempts THEN now() END,
    locked_by = NULL,
    locked_until = NULL
WHERE status = 'running' AND locked_until < now()
RETURNING *;

-- name: RetryDeadJob :one
-- RetryDeadJob sets a dead job back to pending with its attempts reset, and
-- returns its kind. It fails with a unique violation when another job with
-- the same unique_key is active.
UPDATE jobs
SET status = 'pending', attempts = 0, run_at = now(), finished_at = NULL,
    locked_by = NULL, locked_until = NULL
WHERE id = @id AND status = 'dead'
RETURNING kind;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = @id;

-- name: CountJobs :many
-- CountJobs counts the pending, running and dead jobs of each kind.
SELECT kind, status, count(*) AS jobs
FROM jobs
WHERE status IN ('pending', 'running', 'dead')
GROUP BY kind, status;

-- name: ReleaseUniqueKey :execrows
-- ReleaseUniqueKey clears the unique key of a running job that the token
-- still owns, so a new job with the same key can be enqueued while this one
-- finishes.
UPDATE jobs
SET unique_key = NULL
WHERE id = @id AND claim_token = @claim_token::bigint AND status = 'running';
