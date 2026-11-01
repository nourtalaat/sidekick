-- name: InsertActionProposal :one
-- InsertActionProposal stores a proposal as proposed. It returns no row,
-- and stores nothing, when a proposal with the ID exists: the ID is made
-- from the tool call, so a repeated call finds its proposal.
INSERT INTO action_proposals (
  id, investigation_id, action, params, reason, mode, status, idempotency_key, proposed_at, expires_at,
  snapshot, undoable
)
VALUES (
  @id, @investigation_id, @action, @params, @reason, @mode, 'proposed', @idempotency_key, @proposed_at, @expires_at,
  sqlc.narg(snapshot), @undoable
)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: CountInvestigationProposals :one
-- CountInvestigationProposals counts every proposal the model made in an
-- investigation, decided or not. The undo of a proposal is a person's
-- doing, so it does not count.
SELECT count(*) FROM action_proposals WHERE investigation_id = @investigation_id AND undo_of IS NULL;

-- name: GetActionProposal :one
SELECT * FROM action_proposals WHERE id = @id;

-- name: GetActionProposalView :one
-- GetActionProposalView returns a proposal with the route and title of its
-- investigation and the name of who decided it. These are read by primary
-- key for each row returned, after the page is cut, so a page costs the
-- same however many investigations and users there are.
SELECT
  sqlc.embed(p),
  (SELECT i.route FROM investigations i WHERE i.id = p.investigation_id)::text AS route,
  coalesce((SELECT i.alert_title FROM investigations i WHERE i.id = p.investigation_id), '')::text AS alert_title,
  coalesce(
    (SELECT u.name FROM users u WHERE u.id = p.decided_by),
    (SELECT t.name FROM api_tokens t WHERE t.id = p.decided_by),
    '')::text AS decided_by_name
FROM action_proposals p
WHERE p.id = @id;

-- name: LockActionProposal :one
-- LockActionProposal returns a proposal with its investigation's route and
-- holds the proposal's row until the transaction ends.
SELECT sqlc.embed(p), i.route
FROM action_proposals p
JOIN investigations i ON i.id = p.investigation_id
WHERE p.id = @id
FOR UPDATE OF p;

-- name: DecideActionProposal :one
-- DecideActionProposal approves or rejects a proposal that is still
-- proposed and has not expired at now. It returns no row otherwise, for
-- example when another person decided first.
UPDATE action_proposals
SET status = @status,
    decided_by = sqlc.narg(decided_by),
    decided_by_slack_user = sqlc.narg(decided_by_slack_user),
    decided_at = @now::timestamptz
WHERE id = @id AND status = 'proposed' AND expires_at > @now::timestamptz
RETURNING *;

-- name: InsertUndoProposal :one
-- InsertUndoProposal stores the proposal that restores another one. It is
-- approved by the person who asked and already executing. The unique index
-- on the active undos of one proposal fails it with a unique violation
-- when an undo of that proposal is approved or running.
INSERT INTO action_proposals (
  id, investigation_id, action, params, reason, mode, status, idempotency_key, proposed_at, expires_at,
  decided_by, decided_by_slack_user, decided_at, snapshot, undo_of
)
VALUES (
  @id, @investigation_id, @action, @params, '', @mode, 'executing', @idempotency_key, @now::timestamptz, @now::timestamptz,
  sqlc.narg(decided_by), sqlc.narg(decided_by_slack_user), @now::timestamptz, sqlc.narg(snapshot), @undo_of::uuid
)
RETURNING *;

-- name: MarkActionUndone :one
-- MarkActionUndone moves a succeeded proposal to undone. It returns no row
-- for a proposal in any other status.
UPDATE action_proposals SET status = 'undone' WHERE id = @id AND status = 'succeeded'
RETURNING *;

-- name: StartActionExecution :one
-- StartActionExecution moves an approved proposal to executing. It returns
-- no row for a proposal in any other status.
UPDATE action_proposals SET status = 'executing' WHERE id = @id AND status = 'approved'
RETURNING *;

-- name: FinishActionExecution :one
-- FinishActionExecution ends an approved or executing proposal as
-- succeeded or failed, and returns no row for any other. An approved one is
-- failed before it started when the job gave up.
UPDATE action_proposals
SET status = @status,
    executed_at = @now::timestamptz,
    result = sqlc.narg(result),
    error = sqlc.narg(error)
WHERE id = @id AND status IN ('approved', 'executing')
RETURNING *;

-- name: RecordActionAttemptError :exec
-- RecordActionAttemptError keeps the error of a failed attempt that will be
-- retried, so the proposal shows it meanwhile.
UPDATE action_proposals SET error = @error WHERE id = @id AND status = 'executing';

-- name: ExpireActionProposals :many
-- ExpireActionProposals moves up to batch proposals that nobody decided
-- before they expired to expired, oldest first, skipping rows that another
-- transaction holds.
UPDATE action_proposals
SET status = 'expired'
WHERE id IN (
  SELECT e.id FROM action_proposals e
  WHERE e.status = 'proposed' AND e.expires_at <= @now::timestamptz
  ORDER BY e.expires_at
  LIMIT @batch
  FOR UPDATE SKIP LOCKED)
RETURNING id, investigation_id, action;

-- name: ListActionProposals :many
-- ListActionProposals lists proposals with one status, newest first, from
-- the page that starts after the cursor (before_at, before_id). The index
-- on the status and the time gives the order, so a page reads only its own
-- rows. A list of several statuses is the merge of one such page for each.
SELECT
  sqlc.embed(p),
  (SELECT i.route FROM investigations i WHERE i.id = p.investigation_id)::text AS route,
  coalesce((SELECT i.alert_title FROM investigations i WHERE i.id = p.investigation_id), '')::text AS alert_title,
  coalesce(
    (SELECT u.name FROM users u WHERE u.id = p.decided_by),
    (SELECT t.name FROM api_tokens t WHERE t.id = p.decided_by),
    '')::text AS decided_by_name
FROM action_proposals p
WHERE p.status = @status
  AND (p.proposed_at, p.id) < (@before_at::timestamptz, @before_id::uuid)
ORDER BY p.proposed_at DESC, p.id DESC
LIMIT @page_size;

-- name: ListInvestigationActionProposals :many
-- ListInvestigationActionProposals lists the proposals of an
-- investigation, oldest first.
SELECT
  sqlc.embed(p),
  (SELECT i.route FROM investigations i WHERE i.id = p.investigation_id)::text AS route,
  coalesce((SELECT i.alert_title FROM investigations i WHERE i.id = p.investigation_id), '')::text AS alert_title,
  coalesce(
    (SELECT u.name FROM users u WHERE u.id = p.decided_by),
    (SELECT t.name FROM api_tokens t WHERE t.id = p.decided_by),
    '')::text AS decided_by_name
FROM action_proposals p
WHERE p.investigation_id = @investigation_id
ORDER BY p.proposed_at, p.id;
