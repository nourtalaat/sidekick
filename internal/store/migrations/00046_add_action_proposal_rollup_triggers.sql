-- The triggers that mark a dashboard hour for a recount when an action
-- proposal is made or changes status.
--
-- stats_hourly counts the proposals of an investigation in the hour the
-- investigation was created in, in actions_proposed, actions_approved and
-- actions_executed:
--
--   proposed  every proposal.
--   approved  a proposal that a person or the policy approved: its status
--             is approved, executing, succeeded or undone, or it is failed
--             and has a decided_at. Not rejected, expired, or failed
--             without a decision.
--   executed  a proposal that ran and succeeded: succeeded, or undone,
--             since an undone proposal ran before it was undone.
--
-- Like the triggers of the other rollups (see the migration that adds
-- stats_hourly), they mark the bucket in the transaction of the change, so
-- every code path that changes a counted value is covered. What they watch:
--
--   action_proposals, on insert
--     Marks the hour: a new proposal is counted as proposed.
--   action_proposals, on update of status
--     Marks the hour: a decision, a run and its end change what is counted
--     as approved and executed. Other updates, such as the error of a
--     failed attempt that is retried, mark nothing.
--
-- Deleting proposals marks nothing, so the dashboard keeps its history
-- after the retention job deletes the investigations.
--
-- The migration marks the hour of every investigation that has a proposal,
-- so the first run of the job counts the proposals that exist. It is one
-- statement that adds one row to rollup_dirty for each distinct hour that
-- has proposals, and no more.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION rollup_action_proposal_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  created timestamptz;
BEGIN
  SELECT created_at INTO created FROM investigations WHERE id = NEW.investigation_id;

  IF FOUND THEN
    PERFORM rollup_mark('stats_hourly', created);
  END IF;

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER action_proposals_rollup_insert
  AFTER INSERT ON action_proposals
  FOR EACH ROW EXECUTE FUNCTION rollup_action_proposal_changed();

CREATE TRIGGER action_proposals_rollup_update
  AFTER UPDATE ON action_proposals
  FOR EACH ROW
  WHEN (OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION rollup_action_proposal_changed();

-- The proposals that exist before the triggers do.
INSERT INTO rollup_dirty (table_name, bucket)
SELECT DISTINCT 'stats_hourly', date_trunc('hour', i.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
FROM action_proposals p
JOIN investigations i ON i.id = p.investigation_id
ON CONFLICT (table_name, bucket) DO NOTHING;

-- +goose Down
DROP TRIGGER action_proposals_rollup_update ON action_proposals;
DROP TRIGGER action_proposals_rollup_insert ON action_proposals;
DROP FUNCTION rollup_action_proposal_changed();
