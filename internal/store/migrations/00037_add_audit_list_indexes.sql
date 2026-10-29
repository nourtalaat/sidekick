-- +goose NO TRANSACTION
-- The indexes of the audit log list for the filters that the time index
-- cannot serve. The list reads the log newest first, through audit_log_at,
-- and stops after a page; a filter that few rows pass makes that read most
-- of the log before it finds a page. Each index below starts with the
-- filter and goes on in the list's order, so a page is read from the start
-- of one index range. The filters on the investigation, on the kind of
-- actor and on the time alone need no index of their own, and every audit
-- write pays for each index, so there are only these two.
--
-- The indexes are built without blocking writes, which needs a migration
-- outside a transaction. A run that failed part way leaves an invalid
-- index, which running the migration again drops and builds anew.

-- +goose Up
DROP INDEX CONCURRENTLY IF EXISTS audit_log_actor;
CREATE INDEX CONCURRENTLY audit_log_actor ON audit_log (actor_id, at DESC, id DESC) WHERE actor_id IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS audit_log_action;
CREATE INDEX CONCURRENTLY audit_log_action ON audit_log (action, at DESC, id DESC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS audit_log_action;
DROP INDEX CONCURRENTLY IF EXISTS audit_log_actor;
