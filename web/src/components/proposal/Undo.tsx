import { useState } from "react";
import { UndoConflictError, useUndoProposal, type Proposal } from "../../api/proposals";
import { Button } from "../Button";
import { ChangeList, RESTORE } from "./ChangeList";

/**
 * Undo, for a change that worked and can be undone. It asks once before it
 * sends anything. If someone changed one of the fields since, the answer
 * shows what a restore would overwrite and asks again before it restores.
 */
export function Undo({ proposal }: { proposal: Proposal }) {
  const undo = useUndoProposal(proposal.id, proposal.investigationId);
  const [asking, setAsking] = useState(false);
  const conflict = undo.error instanceof UndoConflictError ? undo.error : undefined;

  if (conflict) {
    return (
      <div role="group" aria-label={`Restore ${proposal.action} anyway`} className="sk-proposal__reject">
        <p className="sk-proposal__warning" role="alert">
          {conflict.message}
        </p>
        <ChangeList changes={conflict.changes} heading="What a restore would overwrite" sides={RESTORE} />
        <div className="sk-proposal__buttons">
          <Button variant="destructive" disabled={undo.isPending} onClick={() => undo.mutate(true)}>
            Restore anyway
          </Button>
          <Button
            disabled={undo.isPending}
            onClick={() => {
              setAsking(false);
              undo.reset();
            }}
          >
            Leave it
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="sk-proposal__reject">
      {!asking && (
        <div className="sk-proposal__buttons">
          <Button aria-label={`Undo ${proposal.action}`} onClick={() => setAsking(true)}>
            Undo
          </Button>
        </div>
      )}
      {asking && (
        <div role="group" aria-label={`Confirm undoing ${proposal.action}`} className="sk-proposal__reject">
          <p className="sk-proposal__text">{`Put back the fields this change set in ${proposal.target ?? proposal.action}? If someone changed them since, you see the difference first.`}</p>
          <div className="sk-proposal__buttons">
            <Button variant="primary" disabled={undo.isPending} onClick={() => undo.mutate(false)}>
              Undo it
            </Button>
            <Button disabled={undo.isPending} onClick={() => setAsking(false)}>
              Not now
            </Button>
          </div>
        </div>
      )}
      {undo.isError && !conflict && (
        <p role="alert" className="sk-field__error">
          {undo.error.message}
        </p>
      )}
    </div>
  );
}
