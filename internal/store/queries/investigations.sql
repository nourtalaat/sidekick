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

-- name: GetInvestigation :one
SELECT * FROM investigations WHERE id = @id;

-- name: StartInvestigationRun :one
-- StartInvestigationRun gives the run of an investigation to the job claim
-- with the token fence, and sets it running; the first run sets started_at
-- to now, from the caller's clock. It returns no row when a newer
-- claim owns the run already, or when the investigation has ended; a failed
-- investigation runs again when an admin retries its dead job.
UPDATE investigations
SET fence = @fence, status = 'running', started_at = coalesce(started_at, @now::timestamptz)
WHERE id = @id AND fence < @fence AND status IN ('queued', 'running', 'failed')
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
-- is stale, so a worker that lost its lease writes nothing.
UPDATE investigations
SET next_seq = next_seq + 1,
    input_tokens = input_tokens + @input_tokens,
    output_tokens = output_tokens + @output_tokens,
    cost_micros = cost_micros + @cost_micros,
    tool_calls = tool_calls + @tool_calls,
    first_finding_at = CASE WHEN @finding::boolean THEN coalesce(first_finding_at, now()) ELSE first_finding_at END
WHERE id = @id AND fence = @fence AND status = 'running'
RETURNING next_seq - 1 AS seq, first_finding_at;

-- name: FinishInvestigation :one
-- FinishInvestigation stores the report of a running investigation and
-- ends it, when fence still owns the run. It returns the investigation, or
-- no row when the fence is stale.
UPDATE investigations
SET status = @status, verdict = @verdict, confidence = @confidence, report = @report,
    stop_reason = @stop_reason, finished_at = now()
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
