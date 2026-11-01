-- Action snapshots and Undo. An action that changes a definition in place,
-- such as a monitor, stores the definition before and after the change with
-- the proposal, so an approver reads the difference and an approver can undo
-- the change later. An undo is a proposal of its own that points at the one
-- it restores.
--
-- This only adds columns and indexes, and widens the trigger that keeps what
-- a proposal asks for from changing.

-- +goose Up
ALTER TABLE action_proposals
  -- The action.Snapshot: target, before, after and the provider's version.
  -- Null for an action that does not change a definition in place.
  ADD COLUMN snapshot jsonb,
  -- Set on the proposal that restores another one.
  ADD COLUMN undo_of  uuid REFERENCES action_proposals (id),
  -- Whether the action can undo this proposal once it succeeded. Decided
  -- when it is proposed, so screens need no access to the actions.
  ADD COLUMN undoable boolean NOT NULL DEFAULT false;

-- The foreign key, and finding the undo attempts of one proposal.
CREATE INDEX action_proposals_undo_of ON action_proposals (undo_of) WHERE undo_of IS NOT NULL;
-- At most one undo of a proposal is approved or running at a time, so two
-- clicks cannot both restore.
CREATE UNIQUE INDEX action_proposals_undo_active ON action_proposals (undo_of)
  WHERE undo_of IS NOT NULL AND status IN ('approved', 'executing');

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION action_proposals_reject_change() RETURNS trigger
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
     OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
     OR NEW.snapshot IS DISTINCT FROM OLD.snapshot
     OR NEW.undo_of IS DISTINCT FROM OLD.undo_of
     OR NEW.undoable IS DISTINCT FROM OLD.undoable THEN
    RAISE EXCEPTION 'what an action proposal asks for cannot be changed'
      USING ERRCODE = 'restrict_violation';
  END IF;

  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION action_proposals_reject_change() RETURNS trigger
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

DROP INDEX action_proposals_undo_active;
DROP INDEX action_proposals_undo_of;

ALTER TABLE action_proposals
  DROP COLUMN undoable,
  DROP COLUMN undo_of,
  DROP COLUMN snapshot;
