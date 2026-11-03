import { Link } from "react-router";
import type { Proposal } from "../../api/proposals";
import { formatTime } from "../../format";
import type { SectionProps } from "./section";

/** How a proposal that the route's policy approved is described in place of a person's name. */
const POLICY_APPROVAL = "Approved by policy (read + write route)";

/** The facts of a proposal: its route and times, who decided it, and the proposal it undoes. The investigation is named in the review queue only. */
export function Facts({ proposal, inQueue }: SectionProps) {
  const who = decider(proposal);

  return (
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
        <dt>{proposal.status === "proposed" ? "Decide before" : "Expires"}</dt>
        <dd>{formatTime(proposal.expiresAt)}</dd>
      </div>
      {who && (
        <div>
          <dt>Decided by</dt>
          <dd>{who}</dd>
        </div>
      )}
      {proposal.undoOf && (
        <div>
          <dt>Undoes</dt>
          <dd>The proposal {proposal.undoOf}</dd>
        </div>
      )}
    </dl>
  );
}

/** Who decided: the name of the user or token, or a Slack user's ID, or that the route's policy approved it, or empty. */
function decider(p: Proposal): string {
  if (p.approvedByPolicy) {
    return POLICY_APPROVAL;
  }

  return p.decidedByName ?? (p.decidedBySlackUser ? `Slack user ${p.decidedBySlackUser}` : "");
}
