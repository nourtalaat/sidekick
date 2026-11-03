import { useEffect, useRef, useState } from "react";
import { useDecideProposal, type Proposal } from "../../api/proposals";
import { Button } from "../Button";
import { TextArea } from "../TextArea";
import { deletes } from "./deletes";
import { revisionName } from "./revisionText";

/** The longest note a rejection takes, as the API limits it. */
const MAX_NOTE = 500;

/**
 * Approve and Reject. Each asks once more before it sends anything: Approve
 * to confirm that the action runs with the parameters above, as the Slack
 * button does, and Reject for an optional note. A refused decision shows the
 * reason the server gave.
 */
export function Decide({ proposal }: { proposal: Proposal }) {
  const decide = useDecideProposal(proposal.id, proposal.investigationId);
  const [step, setStep] = useState<"choose" | "approve" | "reject">("choose");
  const [note, setNote] = useState("");
  const confirm = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (step === "approve") {
      confirm.current?.focus();
    }
  }, [step]);

  return (
    <div className="sk-proposal__reject">
      {step === "choose" && (
        <div className="sk-proposal__buttons">
          <Button variant="primary" aria-label={`Approve ${proposal.action}`} onClick={() => setStep("approve")}>
            Approve
          </Button>
          <Button variant="destructive" aria-label={`Reject ${proposal.action}`} onClick={() => setStep("reject")}>
            Reject
          </Button>
        </div>
      )}
      {step === "approve" && (
        <div role="group" aria-label={`Confirm approving ${proposal.action}`} className="sk-proposal__reject">
          <p className="sk-proposal__text">{approveQuestion(proposal)}</p>
          <div className="sk-proposal__buttons">
            <Button ref={confirm} variant="primary" disabled={decide.isPending} onClick={() => decide.mutate({ kind: "approve" })}>
              Approve it
            </Button>
            <Button disabled={decide.isPending} onClick={() => setStep("choose")}>
              Not yet
            </Button>
          </div>
        </div>
      )}
      {step === "reject" && (
        <>
          <TextArea
            label="Why reject it? (optional)"
            value={note}
            maxLength={MAX_NOTE}
            onChange={(e) => setNote(e.target.value)}
            help="Kept in the audit log."
          />
          <div className="sk-proposal__buttons">
            <Button variant="destructive" disabled={decide.isPending} onClick={() => decide.mutate({ kind: "reject", note })}>
              Reject it
            </Button>
            <Button disabled={decide.isPending} onClick={() => setStep("choose")}>
              Keep it
            </Button>
          </div>
        </>
      )}
      {decide.isError && (
        <p role="alert" className="sk-field__error">
          {decide.error.message}
        </p>
      )}
    </div>
  );
}

/** What the confirm step of Approve asks, which says plainly when the action deletes. */
function approveQuestion(p: Proposal): string {
  if (deletes(p)) {
    return `Delete ${p.target ?? "the target"}? Sidekick cannot undo a delete.`;
  }

  if (p.revisions) {
    return `Roll back ${p.action} from ${revisionName(p.revisions.running)} to ${revisionName(p.revisions.goesBackTo)}, as shown above?`;
  }

  if (p.changes && p.changes.length > 0) {
    return `Run ${p.action} with the parameters and the changes above?`;
  }

  return `Run ${p.action} with the parameters above?`;
}
