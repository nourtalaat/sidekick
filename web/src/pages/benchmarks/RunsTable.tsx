import { Link } from "react-router";
import type { BenchRunSummary } from "../../api/bench";
import { StatusBadge } from "../../components/Badge";
import { Table } from "../../components/Table";
import { formatCost, formatDuration, formatTime, MICROS_PER_DOLLAR } from "../../format";
import { formatShare } from "../dashboard/words";

const STARTED_BY_WORDS: Record<string, string> = {
  cli: "Command line",
  ci: "CI",
  gate: "Strategy gate",
};

/** Who started a run, in words; a value this page does not know shows as the API sent it. */
export function startedByOf(triggeredBy: string): string {
  return STARTED_BY_WORDS[triggeredBy] ?? triggeredBy;
}

interface RunsTableProps {
  /** The name of the table region, which says what the rows are. */
  label: string;
  runs: BenchRunSummary[];
}

/**
 * Benchmark runs, one row each, with their headline scores. The name of a run
 * links to its page. The dashboard card and the Benchmarks page both show it.
 */
export function RunsTable({ label, runs }: RunsTableProps) {
  return (
    <Table label={label} className="sk-numbers">
      <thead>
        <tr>
          <th scope="col">Run</th>
          <th scope="col">Result</th>
          <th scope="col">Overall score</th>
          <th scope="col">Verdict accuracy</th>
          <th scope="col">Mean cost per run</th>
          <th scope="col">Mean time per run</th>
          <th scope="col">Scenarios and repeats</th>
          <th scope="col">Started by</th>
        </tr>
      </thead>
      <tbody>
        {runs.map((run) => (
          <tr key={run.id}>
            <th scope="row">
              <Link to={`/benchmarks/${encodeURIComponent(run.id)}`}>{nameOf(run)}</Link>
              <span className="sk-table__secondary">{[run.sidekickVersion, modelsOf(run)].filter(Boolean).join(", ")}</span>
            </th>
            <td>
              <StatusBadge status={run.status === "passed" ? "success" : "danger"}>{run.status === "passed" ? "Passed" : "Failed"}</StatusBadge>
            </td>
            <td>{formatShare(run.metrics.overall)}</td>
            <td>{formatShare(run.metrics.verdictAccuracy)}</td>
            <td>{run.metrics.meanCostPerRunUsd === undefined ? "none yet" : formatCost(run.metrics.meanCostPerRunUsd * MICROS_PER_DOLLAR)}</td>
            <td>{run.metrics.meanDurationMs === undefined ? "none yet" : formatDuration(run.metrics.meanDurationMs)}</td>
            <td>
              {run.scenarioSet}
              <span className="sk-table__secondary">{`${count(run.repeats, "repeat")}, ${count(run.metrics.runs, "run")}`}</span>
            </td>
            <td>{startedByOf(run.triggeredBy)}</td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

/** How a run is named in a list: its label, or its start when it has none. */
export function nameOf(run: BenchRunSummary): string {
  return run.label ? `${run.label}, ${formatTime(run.startedAt)}` : `Run of ${formatTime(run.startedAt)}`;
}

/** The models of the roles of a run, such as "big-model". */
function modelsOf(run: BenchRunSummary): string {
  return [...new Set(Object.values(run.connectors).map((c) => c.model ?? c.connector))].join(", ");
}

function count(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}
