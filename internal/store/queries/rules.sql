-- name: ListListeners :many
-- ListListeners returns every runtime listener, by name.
SELECT * FROM listeners ORDER BY name;

-- name: LockListener :one
-- LockListener returns the runtime listener named name and locks it until
-- the transaction ends.
SELECT * FROM listeners WHERE name = @name FOR UPDATE;

-- name: InsertListener :exec
INSERT INTO listeners (name, slack_channel, match, trusted, enabled, version, updated_by, updated_at)
VALUES (@name, @slack_channel, @match, false, true, 1, @updated_by, @updated_at);

-- name: UpdateListener :exec
UPDATE listeners
SET slack_channel = @slack_channel, match = @match, enabled = @enabled, version = version + 1,
    updated_by = @updated_by, updated_at = @updated_at
WHERE name = @name;

-- name: DeleteListener :execrows
DELETE FROM listeners WHERE name = @name;

-- name: ListRoutes :many
-- ListRoutes returns every runtime route, by name.
SELECT * FROM routes ORDER BY name;

-- name: LockRoute :one
-- LockRoute returns the runtime route named name and locks it until the
-- transaction ends.
SELECT * FROM routes WHERE name = @name FOR UPDATE;

-- name: InsertRoute :exec
INSERT INTO routes (name, priority, spec, enabled, version, updated_by, updated_at)
VALUES (@name, @priority, @spec, @enabled, 1, @updated_by, @updated_at);

-- name: UpdateRoute :exec
UPDATE routes
SET priority = @priority, spec = @spec, enabled = @enabled, version = version + 1,
    updated_by = @updated_by, updated_at = @updated_at
WHERE name = @name;

-- name: DeleteRoute :execrows
DELETE FROM routes WHERE name = @name;

-- name: NotifyRulesChanged :exec
-- NotifyRulesChanged tells every replica to reload the runtime rules.
-- Inside a transaction, Postgres sends the notification when it commits.
SELECT pg_notify('rules_changed', @name::text);

-- name: ListenRulesChanged :exec
LISTEN rules_changed;
