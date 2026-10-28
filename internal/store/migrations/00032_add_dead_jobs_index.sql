-- +goose NO TRANSACTION
-- Admins list dead jobs newest first, a page at a time, to retry them.
-- The index holds only dead jobs, so it stays small however many jobs are
-- done. It is built without blocking writes to jobs, which needs a
-- migration outside a transaction; a run that failed part way leaves an
-- invalid index, which running the migration again drops and builds anew.

-- +goose Up
DROP INDEX CONCURRENTLY IF EXISTS jobs_dead;

CREATE INDEX CONCURRENTLY jobs_dead ON jobs (finished_at DESC, id DESC) WHERE status = 'dead';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS jobs_dead;
