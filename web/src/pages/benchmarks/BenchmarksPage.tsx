import { useBenchRunPages } from "../../api/bench";
import { Button, LinkButton } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { RunsTable } from "./RunsTable";

/**
 * Every benchmark run that sidekick bench run stored, newest first, 25 at a
 * time. Every role from viewer up sees it.
 */
export function BenchmarksPage() {
  const pages = useBenchRunPages();

  const runs = pages.data?.pages.flatMap((page) => page.runs) ?? [];

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Benchmarks</h1>
      <p>The benchmark runs that sidekick bench run stored, newest first.</p>
      {pages.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {pages.isError && !pages.isFetchNextPageError && <QueryError error={pages.error} onRetry={() => void pages.refetch()} />}
      {pages.isSuccess && runs.length === 0 && <EmptyState title="No benchmark runs yet">Runs appear here when sidekick bench run stores its results with --db.</EmptyState>}
      {runs.length > 0 && (
        <>
          {runs.length > 1 && (
            <div className="sk-page__actions">
              <LinkButton to="/benchmarks/compare">Compare two runs</LinkButton>
            </div>
          )}
          <RunsTable label="Benchmark runs" runs={runs} />
          <p role="status">{`Showing ${runs.length} ${runs.length === 1 ? "run" : "runs"}`}</p>
          {pages.isFetchNextPageError && <QueryError error={pages.error} onRetry={() => void pages.fetchNextPage()} />}
          {pages.hasNextPage && (
            <div>
              <Button onClick={() => void pages.fetchNextPage()} disabled={pages.isFetchingNextPage}>
                {pages.isFetchingNextPage ? "Loading" : "Load more"}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}
