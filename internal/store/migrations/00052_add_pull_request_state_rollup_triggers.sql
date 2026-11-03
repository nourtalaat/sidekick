-- The trigger that marks a dashboard hour for a recount when the state of a
-- pull request changes.
--
-- stats_hourly counts a false positive as a noisy alert fixed, in the hour
-- its investigation was created in, when a responder marked the fix applied,
-- or when a monitor pull request that Sidekick opened was merged, or when a
-- monitor change that Sidekick made through a tool's API succeeded and was
-- not undone. The merge is known only from pull_request_states, which a
-- daily job fills, so a change of state there changes what the hour counts.
-- What the trigger watches:
--
--   pull_request_states, on insert
--     Marks the hour: a row can be written as merged.
--   pull_request_states, on update of state
--     Marks the hour: a pull request that was open is now merged or closed.
--     Other updates, such as the time of a check that finds the pull request
--     still open, mark nothing.
--
-- Like the other rollup triggers, it marks the hour in the transaction of the
-- change. Deleting a row marks nothing: the rows go with their proposals,
-- and the dashboard keeps its history after the retention job deletes the
-- investigations.
--
-- The migration marks the hour of every false positive that has a proposal
-- with a fix kind, so the first run of the job counts history by the new
-- rule. It is one statement that calls rollup_mark once for each distinct
-- hour, so the bucket is worked out in one place, as the triggers do. It adds
-- a function and a trigger, and drops nothing.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION rollup_pull_request_state_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  created timestamptz;
BEGIN
  SELECT i.created_at INTO created
  FROM action_proposals p
  JOIN investigations i ON i.id = p.investigation_id
  WHERE p.id = NEW.proposal_id;

  IF FOUND THEN
    PERFORM rollup_mark('stats_hourly', created);
  END IF;

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER pull_request_states_rollup_insert
  AFTER INSERT ON pull_request_states
  FOR EACH ROW EXECUTE FUNCTION rollup_pull_request_state_changed();

CREATE TRIGGER pull_request_states_rollup_update
  AFTER UPDATE ON pull_request_states
  FOR EACH ROW
  WHEN (OLD.state IS DISTINCT FROM NEW.state)
  EXECUTE FUNCTION rollup_pull_request_state_changed();

-- The false positives that have a fix, so history is counted by the new rule.
-- The GROUP BY only finds the distinct hours; rollup_mark works out the bucket.
SELECT rollup_mark('stats_hourly', min(i.created_at))
FROM action_proposals p
JOIN investigations i ON i.id = p.investigation_id
WHERE p.fix_kind IS NOT NULL AND i.verdict = 'false_positive'
GROUP BY date_trunc('hour', i.created_at AT TIME ZONE 'UTC');

-- +goose Down
DROP TRIGGER pull_request_states_rollup_update ON pull_request_states;
DROP TRIGGER pull_request_states_rollup_insert ON pull_request_states;
DROP FUNCTION rollup_pull_request_state_changed();
