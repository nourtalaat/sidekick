import { useId } from "react";
import type { UseQueryResult } from "@tanstack/react-query";
import type { QualityStats, Verdict } from "../../api/stats";
import { VerdictBadge } from "../../components/Badge";
import { Card } from "../../components/Card";
import { Table } from "../../components/Table";
import { BenchRuns } from "./BenchRuns";
import { ChartCard, FILTERS_ID } from "./ChartCard";
import { categoryColor, type ChartSpec } from "./chart";
import { accuracyRows, comparisonRows } from "./data";
import { Loaded } from "./Loaded";
import { VERDICT_OPTIONS, type DashboardFilters } from "./params";
import { formatCount, formatShare } from "./words";
import "./Dashboard.css";

const PERCENT = 100;
const UNKNOWN = "unknown";

interface QualitySectionProps {
  quality: UseQueryResult<QualityStats>;
  /** Every UTC day of the range, as YYYY-MM-DD. */
  days: string[];
  filters: DashboardFilters;
  onChange(filters: DashboardFilters): void;
}

/** Quality: verdict accuracy over time, the confusion matrix, precision and recall, model comparisons and benchmark runs. */
export function QualitySection({ quality, days, filters, onChange }: QualitySectionProps) {
  const headingId = useId();

  return (
    <section aria-labelledby={headingId} className="sk-dashboard__section">
      <h2 id={headingId} className="sk-dashboard__heading">
        Quality
      </h2>
      <Loaded result={quality}>
        {(stats) => (
          <div className="sk-dashboard__grid">
            <ChartCard title="Verdict accuracy over time" summary={accuracySummary(stats)} spec={accuracySpec(stats, days)} />
            <ConfusionMatrix stats={stats} />
            <VerdictScores stats={stats} />
            <ChartCard title="Accuracy by model and strategy" summary={comparisonSummary(stats)} spec={comparisonSpec(stats)} />
          </div>
        )}
      </Loaded>
      <BenchRuns filters={filters} onChange={onChange} />
    </section>
  );
}

function percent(value: number): string {
  return `${Math.round(value)}%`;
}

function accuracySpec(stats: QualityStats, days: string[]): ChartSpec {
  return {
    kind: "line",
    data: accuracyRows(stats, days),
    series: [{ key: "accuracy", label: "Accuracy", color: categoryColor(0) }],
    xLabel: "Day (UTC)",
    format: percent,
    domain: [0, PERCENT],
    tableColumns: [
      { key: "accuracy", label: "Accuracy" },
      { key: "rated", label: "Rated" },
      { key: "correct", label: "Confirmed as given" },
    ],
  };
}

function accuracySummary(stats: QualityStats): string {
  const { rated, correct, accuracy } = stats.totals;

  return `From ${stats.from} to ${stats.to}, people rated ${formatCount(rated)} investigations and confirmed the verdict of ${formatCount(correct)} of them: an accuracy of ${formatShare(accuracy)}.`;
}

function comparisonSpec(stats: QualityStats): ChartSpec {
  return {
    kind: "bar",
    data: comparisonRows(stats),
    series: [{ key: "accuracy", label: "Accuracy", color: categoryColor(0) }],
    xLabel: "Model / strategy",
    format: percent,
    domain: [0, PERCENT],
    tableColumns: [
      { key: "accuracy", label: "Accuracy" },
      { key: "rated", label: "Rated" },
      { key: "correct", label: "Confirmed as given" },
    ],
  };
}

function comparisonSummary(stats: QualityStats): string {
  return `Accuracy of ${stats.comparison.length} combinations of model and strategy, from the investigations people rated.`;
}

/** The verdicts, in the order of the filter, that a matrix has a row or a column for. */
function verdictsOf(stats: QualityStats, side: "predicted" | "confirmed"): Verdict[] {
  const present = new Set(stats.matrix.map((cell) => cell[side]));

  return VERDICT_OPTIONS.map((o) => o.value).filter((v) => present.has(v));
}

/**
 * The verdict Sidekick gave against the verdict people confirmed, as a table of
 * counts. An agreement and a disagreement have each a color and a word, so the
 * colors are never alone.
 */
function ConfusionMatrix({ stats }: { stats: QualityStats }) {
  const rows = verdictsOf(stats, "predicted");
  const columns = verdictsOf(stats, "confirmed");
  const hasUnknown = stats.matrix.some((cell) => cell.confirmed === UNKNOWN);
  const count = (predicted: string, confirmed: string) => stats.matrix.find((c) => c.predicted === predicted && c.confirmed === confirmed)?.n ?? 0;

  if (stats.matrix.length === 0) {
    return (
      <Card title="Verdict given against verdict confirmed" level={3}>
        <p className="sk-chart__empty">
          No data for this range. <a href={`#${FILTERS_ID}`}>Change the range</a>
        </p>
      </Card>
    );
  }

  return (
    <Card title="Verdict given against verdict confirmed" level={3}>
      <p className="sk-chart__summary">
        Each row is a verdict Sidekick gave and each column the verdict people confirmed. A cell that agrees says "agrees", one that does not says
        "differs". Unknown means people rated the verdict wrong without giving the real one.
      </p>
      <Table label="Counts by verdict given and confirmed" className="sk-matrix">
        <thead>
          <tr>
            <th scope="col">Given \ Confirmed</th>
            {columns.map((v) => (
              <th key={v} scope="col">
                {labelOf(v)}
              </th>
            ))}
            {hasUnknown && <th scope="col">Unknown</th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((predicted) => (
            <tr key={predicted}>
              <th scope="row">
                <VerdictBadge verdict={predicted} />
              </th>
              {columns.map((confirmed) => (
                <MatrixCell key={confirmed} n={count(predicted, confirmed)} kind={predicted === confirmed ? "agrees" : "differs"} />
              ))}
              {hasUnknown && <MatrixCell n={count(predicted, UNKNOWN)} kind="unknown" />}
            </tr>
          ))}
        </tbody>
      </Table>
    </Card>
  );
}

function labelOf(verdict: Verdict): string {
  return VERDICT_OPTIONS.find((o) => o.value === verdict)?.label ?? verdict;
}

function MatrixCell({ n, kind }: { n: number; kind: "agrees" | "differs" | "unknown" }) {
  if (n === 0) {
    return <td className="sk-matrix__cell">0</td>;
  }

  return (
    <td className={`sk-matrix__cell sk-matrix__cell--${kind}`}>
      {formatCount(n)}
      <span className="sk-matrix__kind">{kind === "unknown" ? "rated wrong" : kind}</span>
    </td>
  );
}

/** Precision and recall of each verdict that was given or confirmed. */
function VerdictScores({ stats }: { stats: QualityStats }) {
  if (stats.perVerdict.length === 0) {
    return (
      <Card title="Precision and recall by verdict" level={3}>
        <p className="sk-chart__empty">
          No data for this range. <a href={`#${FILTERS_ID}`}>Change the range</a>
        </p>
      </Card>
    );
  }

  return (
    <Card title="Precision and recall by verdict" level={3}>
      <p className="sk-chart__summary">
        Precision is how often a verdict Sidekick gave was confirmed. Recall is how often a verdict people confirmed was the one Sidekick gave.
      </p>
      <Table label="Precision and recall of each verdict" className="sk-numbers">
        <thead>
          <tr>
            <th scope="col">Verdict</th>
            <th scope="col">Given</th>
            <th scope="col">Confirmed</th>
            <th scope="col">Both</th>
            <th scope="col">Precision</th>
            <th scope="col">Recall</th>
          </tr>
        </thead>
        <tbody>
          {stats.perVerdict.map((s) => (
            <tr key={s.verdict}>
              <th scope="row">
                <VerdictBadge verdict={s.verdict} />
              </th>
              <td>{formatCount(s.predicted)}</td>
              <td>{formatCount(s.actual)}</td>
              <td>{formatCount(s.hits)}</td>
              <td>{s.precision === undefined ? "none yet" : formatShare(s.precision)}</td>
              <td>{s.recall === undefined ? "none yet" : formatShare(s.recall)}</td>
            </tr>
          ))}
        </tbody>
      </Table>
    </Card>
  );
}
