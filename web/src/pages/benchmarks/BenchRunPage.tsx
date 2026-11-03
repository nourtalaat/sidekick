import { Link, useParams } from "react-router";
import { useBenchRun } from "../../api/bench";
import { ApiError } from "../../api/client";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { NotFoundPage } from "../NotFoundPage";
import { CostCard } from "./CostCard";
import { FailedThreshold } from "./FailedThreshold";
import { RunHeader } from "./RunHeader";
import { ScenariosCard } from "./ScenariosCard";
import { ScoresCard } from "./ScoresCard";
import { isRunId } from "./runId";
import { ThresholdsCard } from "./ThresholdsCard";
import "./BenchRun.css";

const NOT_FOUND = 404;

/**
 * One benchmark run: its settings, scores with their intervals, thresholds,
 * cost and time, and each scenario with the result of each repeat. Every role
 * from viewer up sees it. An address that is not a run ID, such as
 * /benchmarks/compare, is a page that does not exist and sends no request.
 */
export function BenchRunPage() {
  const { id = "" } = useParams();
  const validId = isRunId(id);
  const query = useBenchRun(validId ? id : undefined);

  if (!validId) {
    return <NotFoundPage />;
  }

  if (query.isPending) {
    return (
      <p role="status" className="sk-loading">
        Loading
      </p>
    );
  }

  if (query.isError) {
    return query.error instanceof ApiError && query.error.status === NOT_FOUND ? <NoRun /> : <QueryError error={query.error} onRetry={() => void query.refetch()} />;
  }

  const run = query.data;

  return (
    <div className="sk-page">
      <RunHeader run={run} />
      {run.status === "failed" && <FailedThreshold thresholds={run.thresholds} />}
      <ScoresCard metrics={run.metrics} intervals={run.intervals} />
      <ThresholdsCard thresholds={run.thresholds} />
      <CostCard metrics={run.metrics} />
      <ScenariosCard results={run.results} />
    </div>
  );
}

function NoRun() {
  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Benchmark run</h1>
      <EmptyState title="No benchmark run has this ID.">
        <Link to="/benchmarks">Back to the benchmark runs</Link>
      </EmptyState>
    </div>
  );
}
