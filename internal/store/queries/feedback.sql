-- name: LockInvestigation :one
-- LockInvestigation returns an investigation and holds a lock on its row
-- until the transaction ends, so two clicks at once confirm it in turn.
SELECT * FROM investigations WHERE id = @id FOR UPDATE;

-- name: UpsertSlackFeedback :one
-- UpsertSlackFeedback stores one Slack user's rating of a target. A user
-- has one rating per target: a click on the other rating changes it, and a
-- click on the same rating again changes nothing and returns no row.
INSERT INTO feedback (
  id, investigation_id, target_kind, target_ref, rating, slack_user_id, channel, created_at, updated_at
)
VALUES (
  @id, @investigation_id, @target_kind, @target_ref, @rating, @slack_user_id::text, 'slack', now(), now()
)
ON CONFLICT (investigation_id, target_kind, target_ref, slack_user_id) WHERE slack_user_id IS NOT NULL
DO UPDATE SET rating = excluded.rating, updated_at = excluded.updated_at
WHERE feedback.rating <> excluded.rating
RETURNING id;

-- name: SetInvestigationConfirmation :exec
-- SetInvestigationConfirmation replaces the confirmed verdict of an
-- investigation. All NULL clears it.
UPDATE investigations
SET confirmed_verdict = @confirmed_verdict,
    confirmed_cause = @confirmed_cause,
    confirmed_by = @confirmed_by,
    confirmed_by_slack_user = @confirmed_by_slack_user,
    confirmed_at = @confirmed_at
WHERE id = @id;

-- name: ListTargetFeedback :many
-- ListTargetFeedback returns the feedback on one target of an
-- investigation, oldest change first.
SELECT rating, user_id, slack_user_id, updated_at
FROM feedback
WHERE investigation_id = @investigation_id AND target_kind = @target_kind AND target_ref = @target_ref
ORDER BY updated_at, id;
