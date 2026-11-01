import type { Proposal } from "../../api/proposals";
import { ProposalCard } from "../../components/ProposalCard";

/**
 * The actions the model proposed in this investigation, oldest first, each
 * with the parameters whole and the model's reason. An approver can decide
 * here; everyone else sees what was proposed and what became of it.
 */
export function ProposalsSection({ proposals, canDecide }: { proposals: readonly Proposal[]; canDecide: boolean }) {
  if (proposals.length === 0) {
    return null;
  }

  return (
    <section aria-labelledby="proposals-heading" className="sk-proposals">
      <h2 id="proposals-heading" className="sk-inv__heading">
        Proposed actions
      </h2>
      {proposals.map((proposal) => (
        <ProposalCard key={proposal.id} proposal={proposal} canDecide={canDecide} />
      ))}
    </section>
  );
}
