-- name: InsertInvestigation :exec
-- InsertInvestigation creates an investigation for a group.
INSERT INTO investigations (
  id, group_id, route, strategy, strategy_version, mode, status, connectors, budget, created_at
)
VALUES (
  @id, @group_id, @route, @strategy, @strategy_version, @mode, @status, @connectors, @budget, now()
);

-- name: GetInvestigationWithEvent :one
-- GetInvestigationWithEvent returns an investigation with the first event
-- of its group, the one that opened it.
SELECT sqlc.embed(investigations), sqlc.embed(events)
FROM investigations
JOIN events ON events.group_id = investigations.group_id
WHERE investigations.id = @id
ORDER BY events.received_at, events.id
LIMIT 1;

-- name: GetEventInvestigation :one
-- GetEventInvestigation returns the investigation of the group the event
-- with event_id is in. It returns no row until the event is grouped.
SELECT sqlc.embed(investigations)
FROM events
JOIN event_groups ON event_groups.id = events.group_id
JOIN investigations ON investigations.id = event_groups.investigation_id
WHERE events.id = @event_id;

-- name: GetInvestigation :one
SELECT * FROM investigations WHERE id = @id;

-- name: StartInvestigationRun :one
-- StartInvestigationRun gives the run of an investigation to the job claim
-- with the token fence, and sets it running; the first run sets started_at
-- to now, from the caller's clock. It returns no row when a newer
-- claim owns the run already, or when the investigation has ended; a failed
-- investigation runs again when an admin retries its dead job. A follow-up
-- run is left to its follow_up job.
UPDATE investigations
SET fence = @fence, status = 'running', started_at = coalesce(started_at, @now::timestamptz)
WHERE id = @id AND fence < @fence AND status IN ('queued', 'running', 'failed') AND run_kind = 'investigate'
RETURNING *;

-- name: StartFollowUpRun :one
-- StartFollowUpRun gives a follow-up run of an investigation to the job
-- claim with the token fence. An investigation that has ended, final or
-- cancelled, gets the next run number and the follow_up run kind, and is
-- set running without the Stop of an earlier run; a follow-up run that is
-- running already keeps its number and its Stop, so a new claim resumes
-- it. It returns no row when a newer claim owns the run, or when the
-- investigation is in neither state.
UPDATE investigations
SET fence = @fence,
    run = CASE WHEN status = 'running' THEN run ELSE run + 1 END,
    cancel_requested_at = CASE WHEN status = 'running' THEN cancel_requested_at END,
    cancel_requested_by = CASE WHEN status = 'running' THEN cancel_requested_by END,
    cancel_requested_by_slack_user = CASE WHEN status = 'running' THEN cancel_requested_by_slack_user END,
    status = 'running',
    run_kind = 'follow_up'
WHERE id = @id AND fence < @fence AND (status IN ('final', 'cancelled') OR (status = 'running' AND run_kind = 'follow_up'))
RETURNING *;

-- name: EndFollowUpRun :one
-- EndFollowUpRun ends the running follow-up run of an investigation whose
-- job is dead, with status, final or cancelled as before the run, and the
-- report that says it could not answer. It returns no row when no
-- follow-up run is running.
UPDATE investigations
SET status = @status, report = @report, finished_at = now(), run_kind = 'investigate'
WHERE id = @id AND status = 'running' AND run_kind = 'follow_up'
RETURNING *;

-- name: GetFirstEvent :one
-- GetFirstEvent returns the event that opened a group, with the number of
-- events in the group.
SELECT sqlc.embed(events), event_groups.event_count
FROM event_groups
JOIN events ON events.group_id = event_groups.id
WHERE event_groups.id = @group_id
ORDER BY events.received_at, events.id
LIMIT 1;

-- name: AdvanceInvestigation :one
-- AdvanceInvestigation takes the next step seq of a running investigation
-- and adds the step's counters, when fence still owns the run. Run it in
-- the transaction that inserts the step. It returns no row when the fence
-- is stale, so a worker that lost its lease writes nothing. It also
-- returns cancel_requested_at, so the run sees a Stop at every step.
UPDATE investigations
SET next_seq = next_seq + 1,
    input_tokens = input_tokens + @input_tokens,
    output_tokens = output_tokens + @output_tokens,
    cost_micros = cost_micros + @cost_micros,
    tool_calls = tool_calls + @tool_calls,
    first_finding_at = CASE WHEN @finding::boolean THEN coalesce(first_finding_at, now()) ELSE first_finding_at END
WHERE id = @id AND fence = @fence AND status = 'running'
RETURNING next_seq - 1 AS seq, first_finding_at, cancel_requested_at;

-- name: FinishInvestigation :one
-- FinishInvestigation stores the report of a running investigation and
-- ends it, when fence still owns the run, and sets the run kind back to
-- investigate, so the end of a follow-up run ends it too. It returns the
-- investigation, or no row when the fence is stale.
UPDATE investigations
SET status = @status, verdict = @verdict, confidence = @confidence, report = @report,
    stop_reason = @stop_reason, finished_at = now(), run_kind = 'investigate'
WHERE id = @id AND fence = @fence AND status = 'running'
RETURNING *;

-- name: FailInvestigation :one
-- FailInvestigation ends an investigation whose job is dead with the report
-- built from its saved steps. It returns no row for an investigation that
-- has ended already.
UPDATE investigations
SET status = 'failed', verdict = @verdict, confidence = @confidence, report = @report,
    stop_reason = @stop_reason, finished_at = now()
WHERE id = @id AND status IN ('queued', 'running')
RETURNING *;

-- name: RequestInvestigationCancel :one
-- RequestInvestigationCancel records that a responder asked to stop a
-- queued or running investigation. It returns no row when the
-- investigation has ended or a stop was asked for already.
UPDATE investigations
SET cancel_requested_at = now(),
    cancel_requested_by = @user_id,
    cancel_requested_by_slack_user = @slack_user_id
WHERE id = @id AND status IN ('queued', 'running') AND cancel_requested_at IS NULL
RETURNING cancel_requested_at;

-- name: NotifyInvestigationCancel :exec
-- NotifyInvestigationCancel tells every replica, once the transaction
-- commits, that a stop was asked for the investigation with id.
SELECT pg_notify('investigation_cancel', @id::text);

-- name: ListenInvestigationCancel :exec
LISTEN investigation_cancel;

-- name: ListCancelRequested :many
-- ListCancelRequested returns which of the investigations with ids have a
-- stop asked for.
SELECT id FROM investigations
WHERE id = ANY(@ids::uuid[]) AND cancel_requested_at IS NOT NULL;
