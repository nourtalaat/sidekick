-- name: CountActionUses :one
-- CountActionUses counts the uses of an action that count against its daily
-- limit for the proposal @id: the proposals of the action that succeeded
-- between day_start and day_end, and the ones that were approved before @id
-- (by decision time, then ID) and have not finished. A proposal approved
-- later does not count against an earlier one, so two runs never both pass
-- a limit the other has taken, and a proposal never waits for a later one.
-- A proposal that was approved before and fails later still counts for the
-- runs that started before it ended; the count errs on the safe side.
SELECT count(*)::bigint AS uses
FROM action_proposals p
WHERE p.action = @action
  AND p.id <> @id
  AND (
    (p.status IN ('succeeded', 'undone')
      AND p.executed_at >= @day_start::timestamptz
      AND p.executed_at < @day_end::timestamptz)
    OR (p.status IN ('approved', 'executing')
      AND (coalesce(p.decided_at, p.proposed_at), p.id) <
          ((SELECT coalesce(m.decided_at, m.proposed_at) FROM action_proposals m WHERE m.id = @id), @id::uuid))
  );
