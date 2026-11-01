import { Link } from "react-router";
import { useStrategyProposal, type StrategyProposalSummary } from "../../api/strategyProposals";
import { QueryError } from "../../components/QueryError";
import { formatTime } from "../../format";
import { DiffView } from "../strategies/DiffView";
import { GateSummary } from "./GateSummary";
import { ProposalActions } from "./ProposalActions";
import { ProposalStatusBadge } from "./proposalLabels";

/** One proposal in the list: who it changes and how it stands, with a button that opens the review of it. */
export function ProposalCard({ proposal, open, onToggle }: { proposal: StrategyProposalSummary; open: boolean; onToggle: () => void }) {
  const panel = `proposal-${proposal.id}`;

  return (
    <article className="sk-proposal" aria-labelledby={`${panel}-title`}>
      <header className="sk-proposal__header">
        <h2 className="sk-proposal__title" id={`${panel}-title`}>
          {proposal.name}
        </h2>
        <ProposalStatusBadge status={proposal.status} />
        <button type="button" className="sk-proposal__toggle" aria-expanded={open} aria-controls={panel} onClick={onToggle}>
          {open ? "Close" : "Review"}
          <span className="sk-visually-hidden"> the change to {proposal.name}</span>
        </button>
      </header>
      <p className="sk-proposal__meta">
        Drafted from version {proposal.baseVersion} after {proposal.triggerCount} wrong ratings, {formatTime(proposal.createdAt)}.
      </p>
      {proposal.statusDetail && <p className="sk-proposal__meta">{proposal.statusDetail}</p>}
      <div id={panel} hidden={!open}>
        {open && <ProposalDetail id={proposal.id} />}
      </div>
    </article>
  );
}

/** The review of one proposal: why, what changes, the feedback, the gate and the decision. */
function ProposalDetail({ id }: { id: string }) {
  const query = useStrategyProposal(id);

  if (query.isPending) {
    return (
      <p role="status" className="sk-loading">
        Loading
      </p>
    );
  }

  if (query.isError) {
    return <QueryError error={query.error} onRetry={() => void query.refetch()} />;
  }

  const p = query.data;

  return (
    <div className="sk-proposal__detail">
      {p.rationale && (
        <section aria-label="Why" className="sk-proposal__section">
          <h3>Why the model changed it</h3>
          <p className="sk-proposal__text">{p.rationale}</p>
        </section>
      )}
      {p.baseBody !== undefined && p.body !== undefined && (
        <section aria-label="The change" className="sk-proposal__section">
          <h3>The change</h3>
          <DiffView before={p.baseBody} after={p.body} label={`Changes to ${p.name}`} />
        </section>
      )}
      <section aria-label="Feedback" className="sk-proposal__section">
        <h3>Wrong ratings it was drafted from</h3>
        {p.feedback.length === 0 ? (
          <p>None are kept with this proposal.</p>
        ) : (
          <ul className="sk-proposal__feedback">
            {p.feedback.map((f) => (
              <li key={f.id}>
                <Link to={`/investigations/${f.investigationId}`}>The {f.targetKind}</Link> was rated {f.rating}.{" "}
                {f.reason ? <span className="sk-proposal__text">{f.reason}</span> : "No reason was given."}
              </li>
            ))}
          </ul>
        )}
      </section>
      {p.gate ? <GateSummary gate={p.gate} /> : <GateWaiting status={p.status} />}
      <ProposalActions proposal={p} />
    </div>
  );
}

/** Says why there is no gate result yet. */
function GateWaiting({ status }: { status: string }) {
  if (status === "benchmarking") {
    return <p>The benchmark gate is running. This page shows its result when it ends.</p>;
  }

  if (status === "pending") {
    return <p>The benchmark gate has not started yet.</p>;
  }

  return null;
}
