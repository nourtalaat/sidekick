import { useId, useState } from "react";
import { Link } from "react-router";
import { useDecideProposal, type Proposal, type ProposalStatus } from "../api/proposals";
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
      </dl>
      <p className="sk-proposal__label" id={paramsId}>
        Parameters, exactly as they will run
      </p>
      <pre className="sk-proposal__params" role="region" aria-labelledby={paramsId} tabIndex={0}>
        {proposal.paramsText}
      </pre>
      <p className="sk-proposal__label">Reason, written by the model and not checked</p>
      <p className="sk-proposal__text">{proposal.reason}</p>
      <Outcome proposal={proposal} />
      {waiting && canDecide && <Decide proposal={proposal} />}
      {waiting && !canDecide && <p className="sk-proposal__note">Only approvers can approve or reject it.</p>}
    </article>
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

/** Approve, and Reject with an optional note. A refused decision shows the reason the server gave. */
function Decide({ proposal }: { proposal: Proposal }) {
  const decide = useDecideProposal(proposal.id, proposal.investigationId);
  const [rejecting, setRejecting] = useState(false);
  const [note, setNote] = useState("");

  return (
    <div className="sk-proposal__reject">
      <div className="sk-proposal__buttons">
        <Button variant="primary" disabled={decide.isPending} aria-label={`Approve ${proposal.action}`} onClick={() => decide.mutate({ kind: "approve" })}>
          Approve
        </Button>
        {!rejecting && (
          <Button variant="destructive" disabled={decide.isPending} aria-label={`Reject ${proposal.action}`} onClick={() => setRejecting(true)}>
            Reject
          </Button>
        )}
      </div>
      {rejecting && (
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
            <Button disabled={decide.isPending} onClick={() => setRejecting(false)}>
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

/** Who decided: the name of the user or token, or a Slack user's ID, or empty. */
function decider(p: Proposal): string {
  return p.decidedByName ?? (p.decidedBySlackUser ? `Slack user ${p.decidedBySlackUser}` : "");
}
