-- Whether an action proposal fixes a noisy monitor, and how. The action says
-- so when the model proposes it (api: it changes a monitor or an alert rule
-- through its tool's API; pull_request: it opens a pull request or merge
-- request that changes a monitor definition), and the answer is stored with
-- the proposal. Null means the proposal fixes no monitor, and is null for
-- an undo.
--
-- This adds a nullable column, fills it for the proposals that exist, and
-- widens the trigger that keeps what a proposal asks for from changing. It
-- drops nothing.

-- +goose Up
ALTER TABLE action_proposals
  ADD COLUMN fix_kind text CHECK (fix_kind IN ('api', 'pull_request'));

-- The proposals that exist, from the target of their snapshot.
--
-- An undo copies the target of the proposal it restores, so undo_of IS NULL
-- keeps undos null. A proposal with no snapshot (made before snapshots
-- existed, or by an action that keeps none) stays null and is not counted.
-- starts_with and not LIKE, because "_" is a wildcard in LIKE.
--
-- At this commit only github.pull_request writes a "github:" target. An
-- action added later that uses the same prefix sets its own kind when it
-- proposes, and does not rely on this statement.
--
-- This runs before the trigger below is replaced, so the trigger does not
-- refuse it.
UPDATE action_proposals SET fix_kind = CASE
    WHEN starts_with(snapshot->>'target', 'github:') THEN 'pull_request'
    ELSE 'api'
  END
WHERE undo_of IS NULL AND snapshot IS NOT NULL AND (
     starts_with(snapshot->>'target', 'datadog:monitor/')
  OR starts_with(snapshot->>'target', 'signoz:rule/')
  OR starts_with(snapshot->>'target', 'grafana:alert_rule/')
  OR starts_with(snapshot->>'target', 'kubernetes:prometheusrule/')
  OR starts_with(snapshot->>'target', 'github:')
);

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
     OR NEW.undoable IS DISTINCT FROM OLD.undoable
     OR NEW.fix_kind IS DISTINCT FROM OLD.fix_kind THEN
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

ALTER TABLE action_proposals DROP COLUMN fix_kind;
