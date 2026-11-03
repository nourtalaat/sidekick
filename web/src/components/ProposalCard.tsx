import type { Proposal, ProposalStatus } from "../api/proposals";
import { StatusBadge, type Status } from "./Badge";
import { Decide } from "./proposal/Decide";
import { sections } from "./proposal/sections";
import { Undo } from "./proposal/Undo";
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

/**
 * One action the model proposed. It shows what an approver must read before
 * deciding: the parameters whole, as the action will run with them, and the
 * model's reason, marked as unchecked. Both are text the model wrote, so they
 * are shown as plain text; the parameters come from the server with hidden
 * characters written as \u escapes. An approver gets Approve and Reject while
 * the proposal waits. The investigation is linked and named in the review
 * queue, where it is not the page's subject. The body is the list of
 * sections in `proposal/sections.tsx`.
 */
export function ProposalCard({ proposal, canDecide, inQueue = false }: { proposal: Proposal; canDecide: boolean; inQueue?: boolean }) {
  const { label, status, spinning } = STATUS[proposal.status];
  const waiting = proposal.status === "proposed";

  return (
    <article className="sk-proposal" aria-label={`Proposed action ${proposal.action}`}>
      <div className="sk-proposal__head">
        <h3 className="sk-proposal__title">{proposal.action}</h3>
        <StatusBadge status={status} spinning={spinning}>
          {label}
        </StatusBadge>
      </div>
      {sections.map((Section, i) => (
        <Section key={i} proposal={proposal} inQueue={inQueue} />
      ))}
      {proposal.undoable && canDecide && <Undo proposal={proposal} />}
      {waiting && canDecide && <Decide proposal={proposal} />}
      {waiting && !canDecide && <p className="sk-proposal__note">Only approvers can approve or reject it.</p>}
    </article>
  );
}
