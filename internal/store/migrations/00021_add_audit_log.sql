-- The audit log: who changed what and when, with the old and new values.
-- Rows are only ever added. A trigger rejects every UPDATE statement, so a
-- bug or a hand-written query cannot rewrite what happened. Deleting old
-- rows is left to the retention job.

-- +goose Up
CREATE TABLE audit_log (
  id               bigserial PRIMARY KEY,
  at               timestamptz NOT NULL,
  -- user, sidekick, policy or system
  actor_kind       text NOT NULL,
  -- For a user, their Slack user ID or Sidekick user ID.
  actor_id         text,
  -- For example listener.created or route.updated.
  action           text NOT NULL,
  target_kind      text,
  target_id        text,
  investigation_id uuid,
  -- {"old": ..., "new": ...}, each left out when there is none.
  details          jsonb NOT NULL DEFAULT '{}'
);

-- For listing the newest entries first.
CREATE INDEX audit_log_at ON audit_log (at DESC);

-- For listing the entries of one investigation.
CREATE INDEX audit_log_investigation ON audit_log (investigation_id)
  WHERE investigation_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION audit_log_reject_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'audit_log rows cannot be updated'
    USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_log_no_update
  BEFORE UPDATE ON audit_log
  FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_update();

-- +goose Down
DROP TABLE audit_log;
DROP FUNCTION audit_log_reject_update();
