import { useState } from "react";
import { ApiError } from "../../api/client";
import { useStrategyProposals, type ProposalFilter } from "../../api/strategyProposals";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { Select, type SelectOption } from "../../components/Select";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import { ProposalCard } from "./ProposalCard";
import "./StrategiesTab.css";

const FILTERS: readonly SelectOption[] = [
  { value: "all", label: "Every status" },
  { value: "passed", label: "Passed the gate" },
  { value: "failed", label: "Did not pass" },
  { value: "pending", label: "Waiting for the gate" },
  { value: "benchmarking", label: "Gate running" },
  { value: "applied", label: "Applied" },
  { value: "approved", label: "Approved (strategy file)" },
  { value: "rejected", label: "Rejected" },
  { value: "drafting", label: "Being drafted" },
  { value: "draft_failed", label: "Draft failed" },
];

/**
 * The changes Sidekick proposed to strategies after repeated wrong feedback.
 * Each is checked by the benchmark gate before an admin can apply it: the
 * current version and the change run on the scenarios that cover the
 * strategy, and the change passes only when it is not clearly worse. An admin
 * reads the change, the model's reason, the feedback and the gate result,
 * then approves, edits or rejects. Strategy changes are for admins.
 */
export function StrategiesTab() {
  const me = useCurrentUser();

  if (!hasRole(me.role, "admin")) {
    return <QueryError error={new ApiError(403, "This needs the admin role.")} />;
  }

  return <Proposals />;
}

/** The filter and the proposals, for someone who may see them. */
function Proposals() {
  const [filter, setFilter] = useState<ProposalFilter>("all");
  const [openId, setOpenId] = useState<string>();
  const proposals = useStrategyProposals(filter);

  return (
    <>
      <p>
        When people keep rating one version of a strategy wrong, Sidekick drafts a change. A benchmark gate runs the current version and the change on
        the same scenarios; only a change that passes it can be applied.
      </p>
      <Select label="Show" options={FILTERS} value={filter} onChange={(event) => setFilter(event.target.value as ProposalFilter)} />
      {proposals.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {proposals.isError && <QueryError error={proposals.error} onRetry={() => void proposals.refetch()} />}
      {proposals.isSuccess && proposals.data.proposals.length === 0 && (
        <EmptyState title="No strategy changes to review">
          A change shows up here when a version of a strategy gets enough wrong ratings, if strategy proposals are turned on.
        </EmptyState>
      )}
      {proposals.isSuccess && proposals.data.proposals.length > 0 && (
        <>
          {proposals.data.nextCursor && <p className="sk-proposal__meta">The newest {proposals.data.proposals.length} are shown.</p>}
          <ul className="sk-proposals">
            {proposals.data.proposals.map((p) => (
              <li key={p.id}>
                <ProposalCard proposal={p} open={openId === p.id} onToggle={() => setOpenId(openId === p.id ? undefined : p.id)} />
              </li>
            ))}
          </ul>
        </>
      )}
    </>
  );
}
