-- +goose NO TRANSACTION
-- One opinion per web user and target, as the index of the Slack users
-- already gives: a new submission replaces the user's earlier one. The
-- list counts ratings by row, so this also keeps one person from rating
-- the same verdict twice.
--
-- The index is built without blocking writes, which needs a migration
-- outside a transaction. A run that failed part way leaves an invalid
-- index, which running the migration again drops and builds anew.

-- +goose Up
DROP INDEX CONCURRENTLY IF EXISTS feedback_user_target;
CREATE UNIQUE INDEX CONCURRENTLY feedback_user_target
  ON feedback (investigation_id, target_kind, target_ref, user_id)
  WHERE user_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS feedback_user_target;
