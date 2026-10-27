-- name: LockRateScope :exec
-- LockRateScope takes the lock of a rate limit scope until the end of the
-- transaction, so the events of one scope are counted and admitted one at
-- a time across every replica.
SELECT pg_advisory_xact_lock(hashtextextended('rate_limit:' || @scope::text, 0));

-- name: GetRateWindow :one
-- GetRateWindow returns how many events a scope admitted after since, the
-- oldest of those admissions (since when there are none), and whether the
-- event was admitted already.
SELECT count(*) FILTER (WHERE admitted_at > @since) AS admitted,
       coalesce(min(admitted_at) FILTER (WHERE admitted_at > @since), @since)::timestamptz AS oldest,
       coalesce(bool_or(event_id = @event_id), false)::boolean AS already
FROM rate_admissions
WHERE scope = @scope;

-- name: AdmitEvent :exec
-- AdmitEvent records that a scope let an event through at admitted_at.
INSERT INTO rate_admissions (scope, event_id, admitted_at)
VALUES (@scope, @event_id, @admitted_at)
ON CONFLICT (scope, event_id) DO NOTHING;

-- name: PruneRateAdmissions :exec
-- PruneRateAdmissions deletes the admissions of a scope that are no longer
-- in its window.
DELETE FROM rate_admissions WHERE scope = @scope AND admitted_at <= @before;

-- name: MarkEventHeld :execrows
-- MarkEventHeld records that a rate limit held an event that is waiting to
-- be routed: status delayed or skipped, with its route and the reason.
UPDATE events
SET status = @status, route = @route, skip_reason = @skip_reason
WHERE id = @id AND status IN ('received', 'delayed');

-- name: MarkEventReceived :exec
-- MarkEventReceived sets a delayed event back to received once a rate
-- limit lets it through, so the router routes it as any other.
UPDATE events SET status = 'received', skip_reason = NULL WHERE id = @id AND status = 'delayed';
