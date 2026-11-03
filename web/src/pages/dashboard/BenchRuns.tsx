import { Link } from "react-router";
import { useBenchComparison, useBenchRuns, type BenchRunSummary } from "../../api/stats";
import { Card } from "../../components/Card";
import { Select } from "../../components/Select";
import { QueryError } from "../../components/QueryError";
import { ComparisonTables } from "../benchmarks/ComparisonTables";
import { nameOf, RunsTable } from "../benchmarks/RunsTable";
import type { DashboardFilters } from "./params";
import "./Dashboard.css";

const NONE = "";

interface BenchRunsProps {
  filters: DashboardFilters;
  onChange(filters: DashboardFilters): void;
}

/**
 * The newest benchmark runs with their headline scores, and a comparison of
 * two of them. The two runs are in the URL, so a comparison can be shared.
 */
export function BenchRuns({ filters, onChange }: BenchRunsProps) {
  const runs = useBenchRuns();
  const comparison = useBenchComparison(filters.compareA, filters.compareB);

  return (
    <Card title="Benchmark runs" level={3}>
      {runs.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {runs.isError && <QueryError error={runs.error} onRetry={() => void runs.refetch()} />}
      {runs.isSuccess && runs.data.runs.length === 0 && <p className="sk-chart__empty">No benchmark runs yet. Runs appear here when sidekick bench run stores its results with --db.</p>}
      {runs.isSuccess && runs.data.runs.length > 0 && (
        <>
          <RunsTable label="Recent benchmark runs" runs={runs.data.runs} />
          <p>
            <Link to="/benchmarks">All benchmark runs</Link>
          </p>
          <div className="sk-dashboard__compare">
            <RunSelect label="Compare run" value={filters.compareA} runs={runs.data.runs} onChange={(id) => onChange({ ...filters, compareA: id })} />
            <RunSelect label="With run" value={filters.compareB} runs={runs.data.runs} onChange={(id) => onChange({ ...filters, compareB: id })} />
          </div>
          {comparison.isPending && comparison.fetchStatus !== "idle" && (
            <p role="status" className="sk-loading">
              Loading
            </p>
          )}
          {comparison.isError && <QueryError error={comparison.error} onRetry={() => void comparison.refetch()} />}
          {comparison.isSuccess && <ComparisonTables comparison={comparison.data} />}
        </>
      )}
    </Card>
  );
}

function RunSelect({ label, value, runs, onChange }: { label: string; value: string | undefined; runs: BenchRunSummary[]; onChange(id: string | undefined): void }) {
  return (
    <Select
      label={label}
      value={value ?? NONE}
      options={[{ value: NONE, label: "Choose a run" }, ...runs.map((run) => ({ value: run.id, label: nameOf(run) }))]}
      onChange={(event) => onChange(event.target.value || undefined)}
    />
  );
}
