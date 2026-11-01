-- name: ListCurrentStrategies :many
-- ListCurrentStrategies returns the current version of every runtime
-- strategy, with its body, by name. It finds each version through the
-- primary key of strategy_versions.
SELECT s.name, s.current_version, s.created_at AS strategy_created_at,
       v.body, v.author, v.reason, v.created_at,
       coalesce(u.name, t.name, '')::text AS author_name
FROM strategies s
JOIN strategy_versions v ON v.name = s.name AND v.version = s.current_version
LEFT JOIN users u ON u.id = v.author
LEFT JOIN api_tokens t ON t.id = v.author
ORDER BY s.name;

-- name: LockStrategy :one
-- LockStrategy returns the runtime strategy named name and locks it until
-- the transaction ends, so two saves of one strategy take turns.
SELECT * FROM strategies WHERE name = @name FOR UPDATE;

-- name: InsertStrategy :exec
INSERT INTO strategies (name, current_version, created_at)
VALUES (@name, 1, @created_at);

-- name: SetStrategyCurrentVersion :exec
UPDATE strategies SET current_version = @current_version WHERE name = @name;

-- name: InsertStrategyVersion :exec
INSERT INTO strategy_versions (name, version, body, author, reason, created_at, proposal_id, bench_run_id)
VALUES (@name, @version, @body, @author, @reason, @created_at, @proposal_id, @bench_run_id);

-- name: GetStrategyVersion :one
-- GetStrategyVersion returns one version of a runtime strategy with its
-- body, through the primary key.
SELECT v.name, v.version, v.body, v.author, v.reason, v.created_at, v.proposal_id, v.bench_run_id,
       coalesce(u.name, t.name, '')::text AS author_name
FROM strategy_versions v
LEFT JOIN users u ON u.id = v.author
LEFT JOIN api_tokens t ON t.id = v.author
WHERE v.name = @name AND v.version = @version;

-- name: ListStrategyVersions :many
-- ListStrategyVersions returns a page of the versions of a runtime
-- strategy, newest first, without their bodies. A page starts below the
-- version before, when it is given. The page is read backwards through the
-- primary key and only its rows are joined.
SELECT p.name, p.version, p.author, p.reason, p.created_at, p.proposal_id, p.bench_run_id,
       coalesce(u.name, t.name, '')::text AS author_name
FROM (
  SELECT v.name, v.version, v.author, v.reason, v.created_at, v.proposal_id, v.bench_run_id
  FROM strategy_versions v
  WHERE v.name = @name AND (sqlc.narg(before)::int IS NULL OR v.version < sqlc.narg(before)::int)
  ORDER BY v.version DESC
  LIMIT @page_size
) AS p
LEFT JOIN users u ON u.id = p.author
LEFT JOIN api_tokens t ON t.id = p.author
ORDER BY p.version DESC;

-- name: NotifyStrategiesChanged :exec
-- NotifyStrategiesChanged tells every replica to reload the runtime
-- strategies. Inside a transaction, Postgres sends the notification when it
-- commits.
SELECT pg_notify('strategies_changed', @name::text);

-- name: ListenStrategiesChanged :exec
LISTEN strategies_changed;
