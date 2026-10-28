-- The queries of the daily retention job. Each delete takes at most batch
-- rows, oldest first, so one statement holds its locks only briefly. The
-- rows are picked by an index scan into an array of their ctids (their
-- places in the table), and deleted by a TID scan of that array, so the
-- plan never joins the table to itself and cannot turn into a scan of the
-- whole table. A row updated in between is left for the next batch.

-- name: DeleteOldEvents :execrows
DELETE FROM events
WHERE ctid = ANY (ARRAY(
  SELECT e.ctid FROM events e
  WHERE e.received_at < @before
  ORDER BY e.received_at
  LIMIT @batch));

-- name: DeleteOldSteps :execrows
DELETE FROM steps
WHERE ctid = ANY (ARRAY(
  SELECT s.ctid FROM steps s
  WHERE s.started_at < @before
  ORDER BY s.started_at
  LIMIT @batch));

-- name: LockOldInvestigations :many
-- LockOldInvestigations locks up to batch investigations created before
-- before, oldest first, with their groups. It leaves out an investigation
-- whose group is still open, which can still take new events, and one
-- that another transaction holds. The group's status is read row by row
-- through its primary key.
SELECT i.id, i.group_id FROM investigations i
WHERE i.created_at < @before
  AND (SELECT g.status FROM event_groups g WHERE g.id = i.group_id) IS DISTINCT FROM 'open'
ORDER BY i.created_at
LIMIT @batch
FOR UPDATE OF i SKIP LOCKED;

-- name: DeleteWatchersOf :execrows
-- DeleteWatchersOf deletes the watchers of the investigations and of their
-- groups, with their checks.
DELETE FROM watchers
WHERE investigation_id = ANY (@investigation_ids::uuid[]) OR group_id = ANY (@group_ids::uuid[]);

-- name: DeleteInvestigations :execrows
-- DeleteInvestigations deletes the investigations. Their steps, inbox rows,
-- output refs, publish states, feedback and watcher changes go with them
-- through their foreign keys.
DELETE FROM investigations WHERE id = ANY (@ids::uuid[]);

-- name: DeleteUnusedEventGroups :execrows
-- DeleteUnusedEventGroups deletes the groups that no investigation or
-- watcher refers to any more. The references are counted group by group
-- through their indexes.
DELETE FROM event_groups g
WHERE g.id = ANY (@ids::uuid[])
  AND (SELECT count(*) FROM investigations i WHERE i.group_id = g.id) = 0
  AND (SELECT count(*) FROM watchers w WHERE w.group_id = g.id) = 0;

-- name: DeleteOldWatcherChecks :execrows
DELETE FROM watcher_checks
WHERE ctid = ANY (ARRAY(
  SELECT c.ctid FROM watcher_checks c
  WHERE c.at < @before
  ORDER BY c.at
  LIMIT @batch));

-- name: AllowAuditLogDelete :exec
-- AllowAuditLogDelete lets the transaction delete audit log rows, which
-- the audit_log_delete_by_retention trigger rejects otherwise. The setting
-- ends with the transaction.
SELECT set_config('sidekick.role', 'retention', true);

-- name: DeleteOldAuditEntries :execrows
-- DeleteOldAuditEntries runs after AllowAuditLogDelete, in the same
-- transaction.
DELETE FROM audit_log
WHERE ctid = ANY (ARRAY(
  SELECT a.ctid FROM audit_log a
  WHERE a.at < @before
  ORDER BY a.at
  LIMIT @batch));

-- name: DeleteDoneJobs :execrows
-- DeleteDoneJobs checks the status again on each row it deletes.
DELETE FROM jobs
WHERE status = 'done' AND ctid = ANY (ARRAY(
  SELECT j.ctid FROM jobs j
  WHERE j.status = 'done' AND j.finished_at < @before::timestamptz
  ORDER BY j.finished_at
  LIMIT @batch));

-- name: DeleteDeadJobs :execrows
-- DeleteDeadJobs checks the status again on each row it deletes, so a dead
-- job that an admin retries meanwhile stays.
DELETE FROM jobs
WHERE status = 'dead' AND ctid = ANY (ARRAY(
  SELECT j.ctid FROM jobs j
  WHERE j.status = 'dead' AND j.finished_at < @before::timestamptz
  ORDER BY j.finished_at
  LIMIT @batch));

-- name: LockRetentionSchedule :exec
-- LockRetentionSchedule takes the transaction-level advisory lock that
-- makes the check of RetentionJobExists and the enqueue that follows it
-- one step across replicas.
SELECT pg_advisory_xact_lock(@lock_key::bigint);

-- name: RetentionJobExists :one
-- RetentionJobExists reports whether a retention job with the unique key
-- exists in any status: pending, running, done or dead.
SELECT EXISTS (
  SELECT 1 FROM jobs WHERE kind = 'retention' AND unique_key = @unique_key::text
) AS found;
