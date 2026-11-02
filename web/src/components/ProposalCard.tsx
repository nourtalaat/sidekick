import { useEffect, useId, useRef, useState } from "react";
import { Link } from "react-router";
import { UndoConflictError, useDecideProposal, useUndoProposal, type Proposal, type ProposalChange, type ProposalStatus } from "../api/proposals";
import { formatTime } from "../format";
import { safeHttpUrl } from "../safeUrl";
import { Button } from "./Button";
import { StatusBadge, type Status } from "./Badge";
import { TextArea } from "./TextArea";
import "./ProposalCard.css";

const STATUS: Record<ProposalStatus, { label: string; status: Status; spinning?: boolean }> = {
  proposed: { label: "Waiting for a decision", status: "neutral" },
  approved: { label: "Approved, waiting to run", status: "info" },
  rejected: { label: "Rejected", status: "neutral" },
  expired: { label: "Expired", status: "warning" },
  executing: { label: "Running", status: "info", spinning: true },
  succeeded: { label: "Done", status: "success" },
  failed: { label: "Failed", status: "danger" },
  undone: { label: "Undone", status: "neutral" },
};

/** How a proposal that the route's policy approved is described in place of a person's name. */
const POLICY_APPROVAL = "Approved by policy (read + write route)";

/** The longest note a rejection takes, as the API limits it. */
const MAX_NOTE = 500;

/**
 * One action the model proposed. It shows what an approver must read before
 * deciding: the parameters whole, as the action will run with them, and the
 * model's reason, marked as unchecked. Both are text the model wrote, so they
 * are shown as plain text; the parameters come from the server with hidden
 * characters written as \u escapes. An approver gets Approve and Reject while
 * the proposal waits. The investigation is linked and named in the review
 * queue, where it is not the page's subject.
 */
export function ProposalCard({ proposal, canDecide, inQueue = false }: { proposal: Proposal; canDecide: boolean; inQueue?: boolean }) {
  const { label, status, spinning } = STATUS[proposal.status];
  const waiting = proposal.status === "proposed";
  const paramsId = useId();

  return (
    <article className="sk-proposal" aria-label={`Proposed action ${proposal.action}`}>
      <div className="sk-proposal__head">
        <h3 className="sk-proposal__title">{proposal.action}</h3>
        <StatusBadge status={status} spinning={spinning}>
          {label}
        </StatusBadge>
      </div>
      <dl className="sk-proposal__facts">
        {inQueue && (
          <div>
            <dt>Investigation</dt>
            <dd>
              <Link to={`/investigations/${proposal.investigationId}`}>{proposal.alertTitle || proposal.investigationId}</Link>
            </dd>
          </div>
        )}
        <div>
          <dt>Route</dt>
          <dd>{proposal.route}</dd>
        </div>
        <div>
          <dt>Proposed</dt>
          <dd>{formatTime(proposal.proposedAt)}</dd>
        </div>
        <div>
          <dt>{waiting ? "Decide before" : "Expires"}</dt>
          <dd>{formatTime(proposal.expiresAt)}</dd>
        </div>
        {decider(proposal) && (
          <div>
            <dt>Decided by</dt>
            <dd>{decider(proposal)}</dd>
          </div>
        )}
        {proposal.undoOf && (
          <div>
            <dt>Undoes</dt>
            <dd>The proposal {proposal.undoOf}</dd>
          </div>
        )}
      </dl>
      <p className="sk-proposal__label" id={paramsId}>
        Parameters, exactly as they will run
      </p>
      <pre className="sk-proposal__params" role="region" aria-labelledby={paramsId} tabIndex={0}>
        {proposal.paramsText}
      </pre>
      {deletes(proposal) && (
        <p className="sk-proposal__warning" role="note">
          This deletes {proposal.target ?? "the target"}. Sidekick cannot undo a delete.
        </p>
      )}
      {proposal.changes && proposal.changes.length > 0 && <Changes changes={proposal.changes} heading="Changes, field by field" />}
      {!proposal.undoOf && (
        <>
          <p className="sk-proposal__label">Reason, written by the model and not checked</p>
          <p className="sk-proposal__text">{proposal.reason}</p>
        </>
      )}
      <Outcome proposal={proposal} />
      {proposal.undoable && canDecide && <Undo proposal={proposal} />}
      {waiting && canDecide && <Decide proposal={proposal} />}
      {waiting && !canDecide && <p className="sk-proposal__note">Only approvers can approve or reject it.</p>}
    </article>
  );
}

/** Whether the proposal removes what it changes: a change of the whole definition with no new value. */
function deletes(p: Proposal): boolean {
  return (p.changes ?? []).some((c) => c.path === "" && c.after === undefined);
}

/** What a side of a change shows when it has no value. */
const NOT_SET = "(not set)";
const REMOVED = "(removed)";

/** The words that name the two values of a change. */
interface Sides {
  before: string;
  after: string;
}

const CHANGED: Sides = { before: "Before", after: "After" };
const RESTORE: Sides = { before: "Now", after: "Restored to" };

/**
 * The difference of a definition, field by field. The values are text that
 * the server wrote with hidden characters spelled out, from the monitoring
 * tool and the model, so they are shown as plain text.
 */
function Changes({ changes, heading, sides = CHANGED }: { changes: ProposalChange[]; heading: string; sides?: Sides }) {
  return (
    <section aria-label={heading} className="sk-changes">
      <h4 className="sk-proposal__label">{heading}</h4>
      <ul className="sk-changes__list">
        {changes.map((c) => (
          <li key={c.path} className="sk-changes__item">
            <code className="sk-changes__path">{c.path === "" ? "(the whole definition)" : c.path}</code>
            <dl className="sk-changes__values">
              <div>
                <dt>{sides.before}</dt>
                <dd className="sk-changes__before">{c.before ?? NOT_SET}</dd>
              </div>
              <div>
                <dt>{sides.after}</dt>
                <dd className="sk-changes__after">{c.after ?? REMOVED}</dd>
              </div>
            </dl>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** What became of a proposal that ran: the summary and links, or why it failed. */
function Outcome({ proposal }: { proposal: Proposal }) {
  if (proposal.result) {
    return (
      <div>
        <p className="sk-proposal__label">Result</p>
        <p className="sk-proposal__text">{proposal.result.summary}</p>
        <ul>
          {(proposal.result.links ?? []).map((link) => {
            const href = safeHttpUrl(link.url);

            return <li key={link.url}>{href ? <a href={href}>{link.title}</a> : link.title}</li>;
          })}
        </ul>
      </div>
    );
  }

  if (proposal.error) {
    return (
      <div>
        <p className="sk-proposal__label">{proposal.status === "failed" ? "Why it failed" : "The last attempt failed"}</p>
        <p className="sk-proposal__text">{proposal.error}</p>
      </div>
    );
  }

  return null;
}

/**
 * Approve and Reject. Each asks once more before it sends anything: Approve
 * to confirm that the action runs with the parameters above, as the Slack
 * button does, and Reject for an optional note. A refused decision shows the
 * reason the server gave.
 */
function Decide({ proposal }: { proposal: Proposal }) {
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

  if (p.changes && p.changes.length > 0) {
    return `Run ${p.action} with the parameters and the changes above?`;
  }

  return `Run ${p.action} with the parameters above?`;
}

/**
 * Undo, for a change that worked and can be undone. It asks once before it
 * sends anything. If someone changed one of the fields since, the answer
 * shows what a restore would overwrite and asks again before it restores.
 */
function Undo({ proposal }: { proposal: Proposal }) {
  const undo = useUndoProposal(proposal.id, proposal.investigationId);
  const [asking, setAsking] = useState(false);
  const conflict = undo.error instanceof UndoConflictError ? undo.error : undefined;

  if (conflict) {
    return (
      <div role="group" aria-label={`Restore ${proposal.action} anyway`} className="sk-proposal__reject">
        <p className="sk-proposal__warning" role="alert">
          {conflict.message}
        </p>
        <Changes changes={conflict.changes} heading="What a restore would overwrite" sides={RESTORE} />
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

/** Who decided: the name of the user or token, or a Slack user's ID, or that the route's policy approved it, or empty. */
function decider(p: Proposal): string {
  if (p.approvedByPolicy) {
    return POLICY_APPROVAL;
  }

  return p.decidedByName ?? (p.decidedBySlackUser ? `Slack user ${p.decidedBySlackUser}` : "");
}
