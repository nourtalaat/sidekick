-- name: LockWatcherStarts :exec
-- LockWatcherStarts takes the transaction-level advisory lock that makes
-- the watcher counts exact across replicas while a watcher starts.
SELECT pg_advisory_xact_lock(@lock_key::bigint);

-- name: CountGroupWatchers :one
-- CountGroupWatchers counts every watcher a group has had, running or
-- ended.
SELECT count(*) FROM watchers WHERE group_id = @group_id;

-- name: CountRunningWatchers :one
-- CountRunningWatchers counts the watchers that run in the deployment. A
-- watcher past its deadline counts no more, even before its tick ends it.
SELECT count(*) FROM watchers WHERE status = 'running' AND deadline > @now::timestamptz;

-- name: InsertWatcher :one
-- InsertWatcher starts a watcher. It returns no row, and inserts nothing,
-- when the group has a watcher with the same name.
INSERT INTO watchers (
  id, group_id, investigation_id, name, origin, started_by_seq, check_type, spec,
  interval_s, passes_to_recover, check_timeout_s, status, started_at, deadline
)
VALUES (
  @id, @group_id, sqlc.narg(investigation_id), @name, @origin, sqlc.narg(started_by_seq), @check_type, @spec,
  @interval_s, @passes_to_recover, @check_timeout_s, 'running', @started_at, @deadline
)
ON CONFLICT ON CONSTRAINT watchers_group_name DO NOTHING
RETURNING *;

-- name: GetWatcherByName :one
SELECT * FROM watchers WHERE group_id = @group_id AND name = @name;

-- name: GetWatcher :one
SELECT * FROM watchers WHERE id = @id;

-- name: GetWatcherForTick :one
-- GetWatcherForTick returns a watcher with the route of its group.
SELECT sqlc.embed(w), g.route
FROM watchers w
JOIN event_groups g ON g.id = w.group_id
WHERE w.id = @id;

-- name: LockWatcher :one
-- LockWatcher returns the watcher with id and locks it until the
-- transaction ends.
SELECT * FROM watchers WHERE id = @id FOR UPDATE;

-- name: GroupHasRunningWatchers :one
-- GroupHasRunningWatchers reports whether a group has a running watcher
-- that has not passed its deadline.
SELECT EXISTS (
  SELECT 1 FROM watchers
  WHERE group_id = @group_id AND status = 'running' AND deadline > @now::timestamptz
) AS running;

-- name: RecordWatcherCheck :one
-- RecordWatcherCheck counts one check of a running watcher: a pass adds 1
-- to passes_in_a_row and keeps when the run of passes started, anything
-- else sets it back to 0. It returns the watcher, whose checks is the seq
-- of the new check.
UPDATE watchers
SET checks = checks + 1,
    last_check_at = @at::timestamptz,
    passes_in_a_row = CASE WHEN @passed::boolean THEN passes_in_a_row + 1 ELSE 0 END,
    passing_since = CASE WHEN @passed::boolean THEN coalesce(passing_since, @at::timestamptz) END
WHERE id = @id AND status = 'running'
RETURNING *;

-- name: InsertWatcherCheck :exec
INSERT INTO watcher_checks (watcher_id, seq, at, result, detail, duration_ms)
VALUES (@watcher_id, @seq, @at, @result, sqlc.narg(detail), @duration_ms);

-- name: EndWatcher :one
-- EndWatcher sets a running watcher to a final status, with the Slack
-- user who stopped it, if one did. A recovered watcher records when its
-- run of passes started. It returns no row for a watcher
-- that has ended already.
UPDATE watchers
SET status = @status::text,
    stop_reason = sqlc.narg(stop_reason)::text,
    stopped_by_slack_user = sqlc.narg(stopped_by_slack_user)::text,
    ended_at = @at::timestamptz,
    recovered_at = CASE WHEN @status::text = 'recovered' THEN coalesce(passing_since, @at::timestamptz) END
WHERE id = @id AND status = 'running'
RETURNING *;

-- name: EndGroupWatchersResolved :many
-- EndGroupWatchersResolved ends the running watchers of a group whose
-- alerts have all recovered: an alert watcher has recovered, as it waits
-- for exactly that, and every other one stops with group_resolved. It
-- returns the watchers it ended.
UPDATE watchers
SET status = CASE WHEN check_type = 'alert' THEN 'recovered' ELSE 'stopped' END,
    stop_reason = CASE WHEN check_type = 'alert' THEN NULL ELSE 'group_resolved' END,
    ended_at = @at::timestamptz,
    recovered_at = CASE WHEN check_type = 'alert' THEN coalesce(passing_since, @at::timestamptz) END
WHERE group_id = @group_id AND status = 'running'
RETURNING *;

-- name: ListGroupAlertStates :many
-- ListGroupAlertStates returns each alert of a group, by its fingerprint,
-- and whether its newest event is a recovery.
SELECT DISTINCT ON (fingerprint) fingerprint, (kind = 'resolved')::boolean AS resolved
FROM events
WHERE group_id = @group_id::uuid AND kind IN ('alert', 'resolved')
ORDER BY fingerprint, received_at DESC, id DESC;

-- name: ListGroupWatchers :many
-- ListGroupWatchers returns every watcher of a group, oldest first, with
-- the detail of its last check.
SELECT sqlc.embed(w), c.detail AS last_detail
FROM watchers w
LEFT JOIN watcher_checks c ON c.watcher_id = w.id AND c.seq = w.checks
WHERE w.group_id = @group_id
ORDER BY w.started_at, w.id;

-- name: GetWatcherWithLastCheck :one
-- GetWatcherWithLastCheck returns a watcher with the detail of its last
-- check.
SELECT sqlc.embed(w), c.detail AS last_detail
FROM watchers w
LEFT JOIN watcher_checks c ON c.watcher_id = w.id AND c.seq = w.checks
WHERE w.id = @id;
