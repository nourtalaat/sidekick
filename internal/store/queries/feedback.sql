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

-- name: GetUserFeedbackRating :one
-- GetUserFeedbackRating returns the rating a signed-in user gave a target,
-- and no row when they gave none.
SELECT rating FROM feedback
WHERE investigation_id = @investigation_id AND target_kind = @target_kind AND target_ref = @target_ref
  AND user_id = @user_id;

-- name: GetSlackFeedbackRating :one
-- GetSlackFeedbackRating returns the rating a Slack user gave a target, and
-- no row when they gave none.
SELECT rating FROM feedback
WHERE investigation_id = @investigation_id AND target_kind = @target_kind AND target_ref = @target_ref
  AND slack_user_id = @slack_user_id::text;

-- name: UpsertUserFeedback :one
-- UpsertUserFeedback stores a signed-in user's feedback on a target. A user
-- has one opinion per target: a new submission replaces the earlier one.
INSERT INTO feedback (
  id, investigation_id, target_kind, target_ref, rating, reason, correct_verdict, correct_cause,
  user_id, channel, created_at, updated_at
)
VALUES (
  @id, @investigation_id, @target_kind, @target_ref, @rating, sqlc.narg(reason), sqlc.narg(correct_verdict),
  sqlc.narg(correct_cause), @user_id, @channel, now(), now()
)
ON CONFLICT (investigation_id, target_kind, target_ref, user_id) WHERE user_id IS NOT NULL
DO UPDATE SET rating = excluded.rating, reason = excluded.reason, correct_verdict = excluded.correct_verdict,
              correct_cause = excluded.correct_cause, channel = excluded.channel, updated_at = excluded.updated_at
RETURNING *;

-- name: UpsertSlackFeedbackDetails :one
-- UpsertSlackFeedbackDetails stores a Slack user's feedback on a target
-- from the Details form. A user has one opinion per target: it replaces the
-- user's earlier rating of the target, whatever they said in it.
INSERT INTO feedback (
  id, investigation_id, target_kind, target_ref, rating, reason, correct_verdict, correct_cause,
  slack_user_id, channel, created_at, updated_at
)
VALUES (
  @id, @investigation_id, @target_kind, @target_ref, @rating, sqlc.narg(reason), sqlc.narg(correct_verdict),
  sqlc.narg(correct_cause), @slack_user_id::text, @channel, now(), now()
)
ON CONFLICT (investigation_id, target_kind, target_ref, slack_user_id) WHERE slack_user_id IS NOT NULL
DO UPDATE SET rating = excluded.rating, reason = excluded.reason, correct_verdict = excluded.correct_verdict,
              correct_cause = excluded.correct_cause, channel = excluded.channel, updated_at = excluded.updated_at
RETURNING *;

-- name: ToolCallExists :one
-- ToolCallExists reports whether a step of an investigation holds the tool
-- call with this ID.
SELECT EXISTS (
  SELECT 1 FROM steps WHERE investigation_id = @investigation_id AND tool_call_id = @tool_call_id
);

-- name: ListFeedbackCounts :many
-- ListFeedbackCounts returns, for each part of an investigation that has
-- feedback, how many people rated it correct and how many wrong. It takes
-- the parts of the report of one run, whose refs start with run_prefix
-- such as "run:2/", and every tool call.
SELECT target_kind, target_ref,
       count(*) FILTER (WHERE rating = 'correct') AS correct,
       count(*) FILTER (WHERE rating = 'wrong') AS wrong
FROM feedback
WHERE investigation_id = @investigation_id
  AND (target_kind = 'tool_call' OR starts_with(target_ref, @run_prefix::text))
GROUP BY target_kind, target_ref
ORDER BY target_kind, target_ref;

-- name: ListUserFeedback :many
-- ListUserFeedback returns what one signed-in user said about the parts
-- ListFeedbackCounts counts.
SELECT target_kind, target_ref, rating, reason, correct_verdict, correct_cause
FROM feedback
WHERE investigation_id = @investigation_id AND user_id = @user_id
  AND (target_kind = 'tool_call' OR starts_with(target_ref, @run_prefix::text))
ORDER BY target_kind, target_ref;
