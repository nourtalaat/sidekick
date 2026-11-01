import { useState } from "react";
import { DECIDED, WAITING, useProposals } from "../../api/proposals";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { ProposalCard } from "../../components/ProposalCard";
import { QueryError } from "../../components/QueryError";
import { Select } from "../../components/Select";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import "./ActionsTab.css";

const FILTERS = [
  { value: "waiting", label: "Waiting for a decision" },
  { value: "decided", label: "Decided" },
];

/**
 * The actions tab of the review queue: the changes the model proposed, to
 * approve or reject. Reading and deciding need the approver role. It shows the
 * proposals that wait for a decision, or those that were decided, newest first.
 */
export function ActionsTab() {
  const me = useCurrentUser();

  if (!hasRole(me.role, "approver")) {
    return <p className="sk-actions__note">Only approvers can see and decide the actions the model proposed.</p>;
  }

  return <Queue />;
}

/** The proposals, for someone who may see them. */
function Queue() {
  const [filter, setFilter] = useState("waiting");
  const proposals = useProposals(filter === "waiting" ? WAITING : DECIDED);

  const items = proposals.data?.pages.flatMap((page) => page.proposals) ?? [];

  return (
    <div className="sk-actions">
      <Select label="Show" value={filter} onChange={(e) => setFilter(e.target.value)} options={FILTERS} />
      {proposals.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {proposals.isError && <QueryError error={proposals.error} onRetry={() => void proposals.refetch()} />}
      {proposals.isSuccess && items.length === 0 && (
        <EmptyState title={filter === "waiting" ? "Nothing waits for a decision" : "No decided actions yet"}>
          {filter === "waiting" ? "When the model proposes an action on a route in approved mode, it shows here." : "Actions you approve or reject show here."}
        </EmptyState>
      )}
      {items.map((proposal) => (
        <ProposalCard key={proposal.id} proposal={proposal} canDecide inQueue />
      ))}
      {proposals.hasNextPage && (
        <Button onClick={() => void proposals.fetchNextPage()} disabled={proposals.isFetchingNextPage}>
          {proposals.isFetchingNextPage ? "Loading" : "Show more"}
        </Button>
      )}
    </div>
  );
}
