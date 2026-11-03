import type { BenchComparison, BenchScenarioRow } from "../../api/bench";
import { StatusBadge } from "../../components/Badge";
import { Table } from "../../components/Table";
import { formatCost, formatDuration, MICROS_PER_DOLLAR } from "../../format";
import { VERDICT_OPTIONS } from "../dashboard/params";
import { formatShare } from "../dashboard/words";
import { nameOf } from "./RunsTable";
import { compareScenarios, scenarioChange, type ScenarioChange } from "./scenarios";

const PERCENT = 100;

const DIFFERENCE_WORDS: Record<string, string> = {
  clear: "Clear difference",
  none: "No clear difference",
  unknown: "Not compared",
};

/** The comparison of two runs: each metric with its change, and each scenario's correct runs. */
export function ComparisonTables({ comparison }: { comparison: BenchComparison }) {
  return (
    <>
      <MetricsTable comparison={comparison} />
      <ScenariosTable comparison={comparison} />
    </>
  );
}

/** Each metric of the two runs with its change and whether the difference is clear. */
export function MetricsTable({ comparison }: { comparison: BenchComparison }) {
  const a = nameOf(comparison.a);
  const b = nameOf(comparison.b);

  return (
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
  );
}

/**
 * Each scenario with the correct runs and the mean root cause score of both
 * runs. Scenarios that went from right to wrong come first, then the ones
 * that went from wrong to right.
 */
export function ScenariosTable({ comparison }: { comparison: BenchComparison }) {
  const a = nameOf(comparison.a);
  const b = nameOf(comparison.b);

  return (
    <Table label={`Scenarios of ${a} and ${b}`} className="sk-numbers">
      <thead>
        <tr>
          <th scope="col">Scenario</th>
          <th scope="col">Expected verdict</th>
          <th scope="col">Change</th>
          <th scope="col">{`${a}: correct`}</th>
          <th scope="col">{`${a}: mean root cause score`}</th>
          <th scope="col">{`${b}: correct`}</th>
          <th scope="col">{`${b}: mean root cause score`}</th>
        </tr>
      </thead>
      <tbody>
        {compareScenarios(comparison.scenarios).map((s) => (
          <tr key={s.scenarioId}>
            <th scope="row">{s.scenarioId}</th>
            <td>{verdictLabel(s.expectedVerdict)}</td>
            <td>
              <ChangeCell row={s} />
            </td>
            <td>{s.a ? `${s.a.correct} of ${s.a.runs}` : "not run"}</td>
            <td>{rootCauseCell(s.a)}</td>
            <td>{s.b ? `${s.b.correct} of ${s.b.runs}` : "not run"}</td>
            <td>{rootCauseCell(s.b)}</td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function ChangeCell({ row }: { row: BenchScenarioRow }) {
  const change = scenarioChange(row);

  switch (change) {
    case "right-to-wrong":
      return <StatusBadge status="danger">Right to wrong</StatusBadge>;
    case "wrong-to-right":
      return <StatusBadge status="success">Wrong to right</StatusBadge>;
    default:
      return <>{plainChange(change, row)}</>;
  }
}

function plainChange(change: ScenarioChange, row: BenchScenarioRow): string {
  if (change === "other") {
    return "Changed in part";
  }

  if (change === "same") {
    return "Same result";
  }

  return row.a ? "Only in Run A" : "Only in Run B";
}

function verdictLabel(verdict: string): string {
  return VERDICT_OPTIONS.find((o) => o.value === verdict)?.label ?? verdict;
}

function rootCauseCell(side: BenchScenarioRow["a"]): string {
  if (!side) {
    return "not run";
  }

  return side.rootCauseMean === undefined ? "not graded" : formatShare(side.rootCauseMean);
}

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
