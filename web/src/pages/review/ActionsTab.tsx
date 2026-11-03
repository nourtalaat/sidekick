import { useSearchParams } from "react-router";
import { useProposalActions, useProposals } from "../../api/proposals";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { ProposalCard } from "../../components/ProposalCard";
import { QueryError } from "../../components/QueryError";
import { Select } from "../../components/Select";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import "./ActionsTab.css";
import { STATUS_FILTERS, WAITING_FILTER, emptyText, emptyTitle, statusFilterOf, type StatusFilter } from "./proposalFilters";

const ACTION_PARAM = "action";
const STATUS_PARAM = "status";
const ANY_ACTION = "";

/**
 * The actions tab of the review queue: the changes the model proposed, to
 * approve or reject. Reading and deciding need the approver role. It shows the
 * proposals that wait for a decision, newest first, or those of one action
 * and one status; both choices are kept in the address.
 */
export function ActionsTab() {
  const me = useCurrentUser();

  if (!hasRole(me.role, "approver")) {
    return <p className="sk-actions__note">Only approvers can see and decide the actions the model proposed.</p>;
  }

  return <Queue />;
}

/** The menus and the proposals, for someone who may see them. */
function Queue() {
  const [params, setParams] = useSearchParams();
  const names = useProposalActions();

  const known = names.data ?? [];
  const asked = params.get(ACTION_PARAM) ?? ANY_ACTION;
  const action = known.includes(asked) ? asked : ANY_ACTION;
  const status = statusFilterOf(params.get(STATUS_PARAM));

  function choose(key: string, value: string, byDefault: string) {
    const next = new URLSearchParams(params);
    if (value === byDefault) {
      next.delete(key);
    } else {
      next.set(key, value);
    }

    setParams(next);
  }

  const actionOptions = [{ value: ANY_ACTION, label: "Any action" }, ...known.map((name) => ({ value: name, label: name }))];
  const statusOptions = STATUS_FILTERS.map((filter) => ({ value: filter.word, label: filter.label }));

  return (
    <div className="sk-actions">
      <div className="sk-actions__filters">
        <Select label="Action" value={action} onChange={(e) => choose(ACTION_PARAM, e.target.value, ANY_ACTION)} options={actionOptions} />
        <Select label="Status" value={status.word} onChange={(e) => choose(STATUS_PARAM, e.target.value, WAITING_FILTER.word)} options={statusOptions} />
      </div>
      {names.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {names.isError && <QueryError error={names.error} onRetry={() => void names.refetch()} />}
      {!names.isPending && <Proposals status={status} action={action} />}
    </div>
  );
}

/** The proposals with one of the statuses of the filter, and of the action unless it is empty. */
function Proposals({ status, action }: { status: StatusFilter; action: string }) {
  const proposals = useProposals(status.statuses, action);

  const items = proposals.data?.pages.flatMap((page) => page.proposals) ?? [];

  return (
    <>
      {proposals.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {proposals.isError && <QueryError error={proposals.error} onRetry={() => void proposals.refetch()} />}
      {proposals.isSuccess && items.length === 0 && <EmptyState title={emptyTitle(status, action)}>{emptyText(status, action)}</EmptyState>}
      {items.map((proposal) => (
        <ProposalCard key={proposal.id} proposal={proposal} canDecide inQueue />
      ))}
      {proposals.hasNextPage && (
        <Button onClick={() => void proposals.fetchNextPage()} disabled={proposals.isFetchingNextPage}>
          {proposals.isFetchingNextPage ? "Loading" : "Show more"}
        </Button>
      )}
    </>
  );
}
