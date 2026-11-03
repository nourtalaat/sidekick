-- name: InsertCIReport :execrows
-- InsertCIReport starts the CI report of a proposal. It adds nothing when the
-- proposal has one already.
INSERT INTO ci_reports (proposal_id, state, started_at)
VALUES (@proposal_id, 'pending', @now)
ON CONFLICT (proposal_id) DO NOTHING;

-- name: GetCIReportForJob :one
-- GetCIReportForJob returns what the CI report job needs about a proposal:
-- its report row, its action, investigation and result, and the parameters
-- the action reads the repository or project from, which for an undo are
-- those of the proposal it undoes.
SELECT c.state, c.checks, c.started_at, c.reported_at,
       p.action, p.investigation_id, p.result,
       coalesce(o.params, p.params)::jsonb AS params
FROM ci_reports c
JOIN action_proposals p ON p.id = c.proposal_id
LEFT JOIN action_proposals o ON o.id = p.undo_of
WHERE c.proposal_id = @proposal_id;

-- name: LockCIReport :one
-- LockCIReport holds the row of a report until the transaction ends and
-- returns when it was reported, if it was.
SELECT reported_at FROM ci_reports WHERE proposal_id = @proposal_id FOR UPDATE;

-- name: RecordCICheck :execrows
-- RecordCICheck stores what a read of a report that is not final found. It
-- changes nothing once the report was made.
UPDATE ci_reports
SET checks = checks + 1, checked_at = @now, detail = @detail
WHERE proposal_id = @proposal_id AND reported_at IS NULL;

-- name: FinishCIReport :execrows
-- FinishCIReport sets the final state of a report. It changes nothing once
-- the report was made, so of two calls only one writes.
UPDATE ci_reports
SET state = @state, detail = @detail, checks = checks + 1, checked_at = @now, reported_at = @now
WHERE proposal_id = @proposal_id AND reported_at IS NULL;

-- name: SetCIReportSeq :exec
-- SetCIReportSeq stores the seq of the thread message of a report.
UPDATE ci_reports SET seq = @seq WHERE proposal_id = @proposal_id;

-- name: ListUnsentCIReports :many
-- ListUnsentCIReports returns the reports of an investigation up to max_seq
-- that have not been sent to sink, oldest first.
SELECT c.proposal_id, c.seq, c.state, c.detail
FROM ci_reports c
JOIN action_proposals p ON p.id = c.proposal_id
WHERE p.investigation_id = @investigation_id
  AND c.seq IS NOT NULL AND c.seq <= @max_seq::bigint
  AND NOT (@sink::text = ANY (c.sent_to))
ORDER BY c.seq;

-- name: MarkCIReportSent :exec
-- MarkCIReportSent records that a report was sent to sink.
UPDATE ci_reports
SET sent_to = array_append(sent_to, @sink::text)
WHERE proposal_id = @proposal_id AND NOT (@sink::text = ANY (sent_to));
