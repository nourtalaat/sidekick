import { useBenchComparison, useBenchRuns, type BenchComparison, type BenchRunSummary } from "../../api/stats";
import { StatusBadge } from "../../components/Badge";
import { Card } from "../../components/Card";
import { Select } from "../../components/Select";
import { Table } from "../../components/Table";
import { QueryError } from "../../components/QueryError";
import { formatCost, formatDuration, formatTime } from "../../format";
import { VERDICT_OPTIONS, type DashboardFilters } from "./params";
import { formatShare } from "./words";
import "./Dashboard.css";

const NONE = "";
const MICROS_PER_DOLLAR = 1_000_000;
const PERCENT = 100;

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
          <RunsTable runs={runs.data.runs} />
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

/** How a run is named in a list: its label, or its start when it has none. */
function nameOf(run: BenchRunSummary): string {
  return run.label ? `${run.label}, ${formatTime(run.startedAt)}` : `Run of ${formatTime(run.startedAt)}`;
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

function RunsTable({ runs }: { runs: BenchRunSummary[] }) {
  return (
    <Table label="Recent benchmark runs" className="sk-numbers">
      <thead>
        <tr>
          <th scope="col">Run</th>
          <th scope="col">Result</th>
          <th scope="col">Overall score</th>
          <th scope="col">Verdict accuracy</th>
          <th scope="col">Mean cost per run</th>
          <th scope="col">Mean time per run</th>
          <th scope="col">Scenarios</th>
        </tr>
      </thead>
      <tbody>
        {runs.map((run) => (
          <tr key={run.id}>
            <th scope="row">
              {nameOf(run)}
              <span className="sk-table__secondary">{[run.sidekickVersion, modelsOf(run)].filter(Boolean).join(", ")}</span>
            </th>
            <td>
              <StatusBadge status={run.status === "passed" ? "success" : "danger"}>{run.status === "passed" ? "Passed" : "Failed"}</StatusBadge>
            </td>
            <td>{formatShare(run.metrics.overall)}</td>
            <td>{formatShare(run.metrics.verdictAccuracy)}</td>
            <td>{run.metrics.meanCostPerRunUsd === undefined ? "none yet" : formatCost(run.metrics.meanCostPerRunUsd * MICROS_PER_DOLLAR)}</td>
            <td>{run.metrics.meanDurationMs === undefined ? "none yet" : formatDuration(run.metrics.meanDurationMs)}</td>
            <td>{`${run.scenarioSet}, ${run.metrics.runs} runs`}</td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

/** The models of the roles of a run, such as "big-model". */
function modelsOf(run: BenchRunSummary): string {
  return [...new Set(Object.values(run.connectors).map((c) => c.model ?? c.connector))].join(", ");
}

const DIFFERENCE_WORDS: Record<string, string> = {
  clear: "Clear difference",
  none: "No clear difference",
  unknown: "Not compared",
};

/** The value of a metric as the table shows it: shares as percentages, the mean cost and time in their units. */
function metricCell(key: string, value: number | undefined): string {
  if (value === undefined) {
    return "none yet";
  }

  switch (key) {
    case "meanCostPerRunUSD":
      return formatCost(value * MICROS_PER_DOLLAR);
    case "meanDurationMs":
      return formatDuration(value);
    default:
      return formatShare(value);
  }
}

/** The change of a metric, with its sign and its unit. */
function changeCell(key: string, change: number | undefined): string {
  if (change === undefined) {
    return "none";
  }

  const sign = change > 0 ? "+" : change < 0 ? "-" : "";
  const size = Math.abs(change);

  switch (key) {
    case "meanCostPerRunUSD":
      return `${sign}${formatCost(size * MICROS_PER_DOLLAR)}`;
    case "meanDurationMs":
      return `${sign}${formatDuration(size)}`;
    default:
      return `${sign}${Math.round(size * PERCENT)} points`;
  }
}

/** The comparison of two runs: each metric with its change, and each scenario's correct runs. */
function ComparisonTables({ comparison }: { comparison: BenchComparison }) {
  const a = nameOf(comparison.a);
  const b = nameOf(comparison.b);
  const verdictLabel = (verdict: string) => VERDICT_OPTIONS.find((o) => o.value === verdict)?.label ?? verdict;

  return (
    <>
      <Table label={`Metrics of ${a} and ${b}`} className="sk-numbers">
        <caption className="sk-table__caption">{`Comparison of ${a} with ${b}. A difference is clear when the confidence intervals of the two runs do not overlap.`}</caption>
        <thead>
          <tr>
            <th scope="col">Metric</th>
            <th scope="col">{a}</th>
            <th scope="col">{b}</th>
            <th scope="col">Change</th>
            <th scope="col">Difference</th>
          </tr>
        </thead>
        <tbody>
          {comparison.metrics.map((m) => (
            <tr key={m.key}>
              <th scope="row">{m.label}</th>
              <td>{metricCell(m.key, m.a)}</td>
              <td>{metricCell(m.key, m.b)}</td>
              <td>{changeCell(m.key, m.change)}</td>
              <td>{DIFFERENCE_WORDS[m.difference] ?? m.difference}</td>
            </tr>
          ))}
        </tbody>
      </Table>
      <Table label={`Scenarios of ${a} and ${b}`} className="sk-numbers">
        <thead>
          <tr>
            <th scope="col">Scenario</th>
            <th scope="col">Expected verdict</th>
            <th scope="col">{`${a}: correct`}</th>
            <th scope="col">{`${b}: correct`}</th>
          </tr>
        </thead>
        <tbody>
          {comparison.scenarios.map((s) => (
            <tr key={s.scenarioId}>
              <th scope="row">{s.scenarioId}</th>
              <td>{verdictLabel(s.expectedVerdict)}</td>
              <td>{s.a ? `${s.a.correct} of ${s.a.runs}` : "not run"}</td>
              <td>{s.b ? `${s.b.correct} of ${s.b.runs}` : "not run"}</td>
            </tr>
          ))}
        </tbody>
      </Table>
    </>
  );
}
