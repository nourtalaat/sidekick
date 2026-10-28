import { useSearchParams } from "react-router";
import { useInvestigationList, type ListFilters } from "../../api/investigations";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { COMPACT_PARAM, filtersFromParams, paramsFromFilters } from "./filters";
import { FilterBar } from "./FilterBar";
import { InvestigationTable } from "./InvestigationTable";
import "./InvestigationsPage.css";

/** The list of investigations, newest first, with its filters kept in the URL. */
export default function InvestigationsPage() {
  const [params, setParams] = useSearchParams();
  const filters = filtersFromParams(params);
  const compact = params.get(COMPACT_PARAM) === "1";
  const list = useInvestigationList(filters);

  const setFilters = (next: ListFilters) => setParams(paramsFromFilters(next, compact), { replace: true });
  const setCompact = (on: boolean) => setParams(paramsFromFilters(filters, on), { replace: true });

  const rows = list.data?.pages.flatMap((page) => page.investigations) ?? [];

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Investigations</h1>
      <FilterBar filters={filters} onChange={setFilters} />
      <label className="sk-compact">
        <input type="checkbox" checked={compact} onChange={(event) => setCompact(event.target.checked)} />
        Compact rows
      </label>
      {list.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {list.isError && <QueryError error={list.error} onRetry={() => void list.refetch()} />}
      {list.isSuccess && rows.length === 0 && (
        <EmptyState title="No investigations">Nothing matches these filters. Change them, or wait for the next alert.</EmptyState>
      )}
      {rows.length > 0 && <InvestigationTable rows={rows} compact={compact} />}
      {list.hasNextPage && (
        <div className="sk-more">
          <Button onClick={() => void list.fetchNextPage()} disabled={list.isFetchingNextPage}>
            {list.isFetchingNextPage ? "Loading more" : "Load more"}
          </Button>
        </div>
      )}
    </div>
  );
}
