-- Action proposals: a change the model asked for, that a person approves or
-- rejects before Sidekick makes it. A proposal moves from proposed to
-- approved or rejected, or to expired when nobody decides in time; an
-- approved one is executing while the execute_action job runs, and ends
-- as succeeded or failed.
--
-- What the approver sees is what runs, so a trigger rejects every change
-- to the columns that say what the proposal is.

-- +goose Up
CREATE TABLE action_proposals (
  id                    uuid PRIMARY KEY,
  investigation_id      uuid NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
  -- The name of the action in the actions section of the config.
  action                text NOT NULL,
  -- The parameters as the model wrote them, checked and rewritten in a
  -- fixed form. Untrusted text.
  params                jsonb NOT NULL,
  -- Why the model proposed it. Untrusted text.
  reason                text NOT NULL DEFAULT '',
  -- The action mode of the investigation: approved, or read-write.
  mode                  text NOT NULL,
  status                text NOT NULL CHECK (status IN
    ('proposed', 'approved', 'rejected', 'expired', 'executing', 'succeeded', 'failed', 'undone')),
  -- Passed to the action when it runs, so a repeat after a crash finds
  -- what the first run made.
  idempotency_key       text NOT NULL UNIQUE,
  proposed_at           timestamptz NOT NULL,
  expires_at            timestamptz NOT NULL,
  -- Who approved or rejected it: a Sidekick user or API token, or a Slack
  -- user with none. policy is not a person, and has neither.
  decided_by            uuid,
  decided_by_slack_user text,
  decided_at            timestamptz,
  -- When the execution ended.
  executed_at           timestamptz,
  -- The action.Result, once it succeeded.
  result                jsonb,
  -- Why it failed, or the last failed attempt while it is retried.
  error                 text
);

-- Finding the proposals that have expired.
CREATE INDEX action_proposals_expiry ON action_proposals (expires_at) WHERE status = 'proposed';
-- Listing the review queue by status, newest first.
CREATE INDEX action_proposals_status_proposed ON action_proposals (status, proposed_at DESC, id DESC);
-- The proposals of one investigation, and the foreign key.
CREATE INDEX action_proposals_investigation ON action_proposals (investigation_id, proposed_at);

-- +goose StatementBegin
CREATE FUNCTION action_proposals_reject_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.investigation_id IS DISTINCT FROM OLD.investigation_id
     OR NEW.action IS DISTINCT FROM OLD.action
     OR NEW.params IS DISTINCT FROM OLD.params
     OR NEW.reason IS DISTINCT FROM OLD.reason
     OR NEW.mode IS DISTINCT FROM OLD.mode
     OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
     OR NEW.proposed_at IS DISTINCT FROM OLD.proposed_at
     OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
    RAISE EXCEPTION 'what an action proposal asks for cannot be changed'
      USING ERRCODE = 'restrict_violation';
  END IF;

  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER action_proposals_immutable
  BEFORE UPDATE ON action_proposals
  FOR EACH ROW EXECUTE FUNCTION action_proposals_reject_change();

-- +goose Down
DROP TABLE action_proposals;
DROP FUNCTION action_proposals_reject_change();
