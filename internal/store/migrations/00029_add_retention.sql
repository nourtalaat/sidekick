-- +goose NO TRANSACTION
-- What the daily retention job needs: an index for each delete, so every
-- batch finds its oldest rows without a scan, and a guard on the audit log.
--
-- The indexes are built without blocking writes, because steps and jobs
-- can be large on a live install, which needs a migration outside a
-- transaction. A run that failed part way can leave an invalid index
-- behind, which running the migration again drops first; the trigger is
-- replaced the same way.
--
-- The audit log trigger rejects DELETE and TRUNCATE unless the transaction
-- set sidekick.role to retention with SET LOCAL, which only the retention
-- job does. It guards against a bug or a stray query, not against someone
-- with access to the database, who can set the same value or drop the
-- trigger.

-- +goose Up
-- Investigations, oldest first.
DROP INDEX CONCURRENTLY IF EXISTS investigations_created;
CREATE INDEX CONCURRENTLY investigations_created ON investigations (created_at);

-- Steps, oldest first.
DROP INDEX CONCURRENTLY IF EXISTS steps_started;
CREATE INDEX CONCURRENTLY steps_started ON steps (started_at);

-- Every inbox row of an investigation, delivered or not, for the cascade
-- when the investigation is deleted.
DROP INDEX CONCURRENTLY IF EXISTS investigation_inbox_investigation;
CREATE INDEX CONCURRENTLY investigation_inbox_investigation ON investigation_inbox (investigation_id);

-- Done and dead jobs, oldest first.
DROP INDEX CONCURRENTLY IF EXISTS jobs_finished;
CREATE INDEX CONCURRENTLY jobs_finished ON jobs (status, finished_at) WHERE status IN ('done', 'dead');

-- The retention jobs in any status, so a replica can tell whether today's
-- job has run already.
DROP INDEX CONCURRENTLY IF EXISTS jobs_retention_key;
CREATE INDEX CONCURRENTLY jobs_retention_key ON jobs (unique_key) WHERE kind = 'retention';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_reject_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF current_setting('sidekick.role', true) IS DISTINCT FROM 'retention' THEN
    RAISE EXCEPTION 'audit_log rows are deleted only by the retention job'
      USING ERRCODE = 'restrict_violation';
  END IF;

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS audit_log_delete_by_retention ON audit_log;
CREATE TRIGGER audit_log_delete_by_retention
  BEFORE DELETE OR TRUNCATE ON audit_log
  FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_delete();

-- +goose Down
DROP TRIGGER IF EXISTS audit_log_delete_by_retention ON audit_log;
DROP FUNCTION IF EXISTS audit_log_reject_delete();
DROP INDEX CONCURRENTLY IF EXISTS jobs_retention_key;
DROP INDEX CONCURRENTLY IF EXISTS jobs_finished;
DROP INDEX CONCURRENTLY IF EXISTS investigation_inbox_investigation;
DROP INDEX CONCURRENTLY IF EXISTS steps_started;
DROP INDEX CONCURRENTLY IF EXISTS investigations_created;
