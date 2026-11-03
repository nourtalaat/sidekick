import { useId } from "react";
import type { UseQueryResult } from "@tanstack/react-query";
import type { QualityStats, Verdict } from "../../api/stats";
import { VerdictBadge } from "../../components/Badge";
import { Card } from "../../components/Card";
import { Select } from "../../components/Select";
import { Table } from "../../components/Table";
import { BenchRuns } from "./BenchRuns";
import { ChartCard, FILTERS_ID } from "./ChartCard";
import { categoryColor, type ChartSpec } from "./chart";
import { accuracyRows, comparisonRows, scoreRows } from "./data";
import { Loaded } from "./Loaded";
import { DEFAULT_SCORE_VERDICT, VERDICT_OPTIONS, type DashboardFilters } from "./params";
import { formatCount, formatShare } from "./words";
import "./Dashboard.css";

const PERCENT = 100;
const UNKNOWN = "unknown";

/** Said where recall is left out, because the page's Verdict filter keeps only the investigations where Sidekick gave that verdict. */
const RECALL_NEEDS_EVERY_VERDICT = "Recall needs every verdict. Clear the verdict filter to see it.";

interface QualitySectionProps {
  quality: UseQueryResult<QualityStats>;
  /** Every UTC day of the range, as YYYY-MM-DD. */
  days: string[];
  filters: DashboardFilters;
  onChange(filters: DashboardFilters): void;
}

/** Quality: verdict accuracy over time, precision and recall over time, the confusion matrix, precision and recall by verdict, model comparisons and benchmark runs. */
export function QualitySection({ quality, days, filters, onChange }: QualitySectionProps) {
  const headingId = useId();
  const filtered = filters.verdict !== undefined;
  const scoreVerdict = filters.verdict ?? filters.scoreVerdict ?? DEFAULT_SCORE_VERDICT;

  return (
    <section aria-labelledby={headingId} className="sk-dashboard__section">
      <h2 id={headingId} className="sk-dashboard__heading">
        Quality
      </h2>
      <Loaded result={quality}>
        {(stats) => (
          <div className="sk-dashboard__grid">
            <ChartCard title="Verdict accuracy over time" summary={accuracySummary(stats)} spec={accuracySpec(stats, days)} />
            <ChartCard
              title="Precision and recall over time"
              summary={scoresSummary(stats, scoreVerdict, filtered)}
              spec={scoresSpec(stats, days, scoreVerdict, filtered)}
              note={filtered ? RECALL_NEEDS_EVERY_VERDICT : undefined}
              aside={filtered ? undefined : <ScoreVerdictMenu verdict={scoreVerdict} filters={filters} onChange={onChange} />}
            />
            <ConfusionMatrix stats={stats} />
            <VerdictScores stats={stats} filtered={filtered} />
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
      { key: "rated", label: "Rated", format: formatCount },
      { key: "correct", label: "Confirmed as given", format: formatCount },
    ],
  };
}

function accuracySummary(stats: QualityStats): string {
  const { rated, correct, accuracy } = stats.totals;

  return `From ${stats.from} to ${stats.to}, people rated ${formatCount(rated)} investigations and confirmed the verdict of ${formatCount(correct)} of them: an accuracy of ${formatShare(accuracy)}.`;
}

/** With `precisionOnly`, recall and the count of confirmed verdicts are left out: the page's verdict filter makes them too high. */
function scoresSpec(stats: QualityStats, days: string[], verdict: Verdict, precisionOnly: boolean): ChartSpec {
  const precision = { key: "precision", label: "Precision" };
  const recall = { key: "recall", label: "Recall" };
  const given = { key: "given", label: "Given", format: formatCount };
  const confirmed = { key: "confirmed", label: "Confirmed", format: formatCount };
  const agreed = { key: "agreed", label: "Agreed", format: formatCount };

  return {
    kind: "line",
    data: scoreRows(stats, days, verdict),
    series: precisionOnly ? [{ ...precision, color: categoryColor(0) }] : [{ ...precision, color: categoryColor(0) }, { ...recall, color: categoryColor(1) }],
    xLabel: "Day (UTC)",
    format: percent,
    domain: [0, PERCENT],
    tableColumns: precisionOnly ? [precision, given, agreed] : [precision, recall, given, confirmed, agreed],
  };
}

function scoresSummary(stats: QualityStats, verdict: Verdict, precisionOnly: boolean): string {
  const label = labelOf(verdict);
  const score = stats.perVerdict.find((s) => s.verdict === verdict);

  if (!score) {
    return `${label}: no verdicts given or confirmed in this range.`;
  }

  const precision = score.precision === undefined ? "no precision yet" : `precision ${formatShare(score.precision)}`;
  const recall = score.recall === undefined ? "no recall yet" : `recall ${formatShare(score.recall)}`;

  if (precisionOnly) {
    return `${label}: ${precision} over the range, from ${verdictCount(score.predicted)} given.`;
  }

  return `${label}: ${precision} and ${recall} over the range, from ${verdictCount(score.predicted)} given and ${formatCount(score.actual)} confirmed.`;
}

function verdictCount(n: number): string {
  return `${formatCount(n)} ${n === 1 ? "verdict" : "verdicts"}`;
}

/** The menu on the card that chooses the verdict to score; the page's Verdict filter is another choice. */
function ScoreVerdictMenu({ verdict, filters, onChange }: { verdict: Verdict; filters: DashboardFilters; onChange(filters: DashboardFilters): void }) {
  const choose = (value: string) => {
    const chosen = VERDICT_OPTIONS.find((o) => o.value === value)?.value;
    onChange({ ...filters, scoreVerdict: chosen === DEFAULT_SCORE_VERDICT ? undefined : chosen });
  };

  return (
    <div className="sk-chart__controls">
      <Select label="Verdict" value={verdict} options={VERDICT_OPTIONS} onChange={(event) => choose(event.target.value)} />
    </div>
  );
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
      { key: "rated", label: "Rated", format: formatCount },
      { key: "correct", label: "Confirmed as given", format: formatCount },
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

/** Precision and recall of each verdict that was given or confirmed. With the page's verdict filter set, recall is left out and the card says why. */
function VerdictScores({ stats, filtered }: { stats: QualityStats; filtered: boolean }) {
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
      {filtered && <p className="sk-chart__note">{RECALL_NEEDS_EVERY_VERDICT}</p>}
      <Table label="Precision and recall of each verdict" className="sk-numbers">
        <thead>
          <tr>
            <th scope="col">Verdict</th>
            <th scope="col">Given</th>
            <th scope="col">Confirmed</th>
            <th scope="col">Both</th>
            <th scope="col">Precision</th>
            {!filtered && <th scope="col">Recall</th>}
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
              {!filtered && <td>{s.recall === undefined ? "none yet" : formatShare(s.recall)}</td>}
            </tr>
          ))}
        </tbody>
      </Table>
    </Card>
  );
}
