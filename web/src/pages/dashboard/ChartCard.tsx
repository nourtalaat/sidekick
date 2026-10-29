import { lazy, Suspense, useId, useState, type ReactNode } from "react";
import { Button } from "../../components/Button";
import { Card } from "../../components/Card";
import { hasData, type ChartSpec } from "./chart";
import "./Chart.css";

// The chart library is the largest part of the bundle, so only the dashboard loads it, and only
// when a chart has something to draw.
const ChartBody = lazy(() => import("./ChartBody"));

/** The id of the filter form, which "Change the range" in an empty chart links to. */
export const FILTERS_ID = "dashboard-filters";

interface ChartCardProps {
  title: string;
  /** A sentence that says what the chart shows, for readers who do not see it. */
  summary: string;
  spec: ChartSpec;
  /** A line under the title, such as a filter that does not apply. */
  note?: string;
  /** Shown with the chart, such as a status badge next to the daily cap. */
  aside?: ReactNode;
}

/**
 * A card with a chart that follows the chart rules of the design language: a
 * text summary, a legend when there are two series or more, a tooltip, and
 * a table view of the same numbers. A chart with nothing to draw says so
 * and links to the filters, to change the range.
 */
export function ChartCard({ title, summary, spec, note, aside }: ChartCardProps) {
  const [showTable, setShowTable] = useState(false);

  return (
    <Card title={title} level={3}>
      {note && <p className="sk-chart__note">{note}</p>}
      {aside}
      {hasData(spec) ? (
        <figure className="sk-chart">
          <figcaption className="sk-chart__summary">{summary}</figcaption>
          {spec.series.length > 1 && <Legend spec={spec} />}
          <Button onClick={() => setShowTable(!showTable)}>
            {showTable ? "Show as chart" : "Show as table"}
          </Button>
          {showTable ? (
            <ChartTable title={title} spec={spec} />
          ) : (
            <Suspense
              fallback={
                <p role="status" className="sk-loading">
                  Loading chart
                </p>
              }
            >
              <ChartBody spec={spec} />
            </Suspense>
          )}
        </figure>
      ) : (
        <p className="sk-chart__empty">
          No data for this range. <a href={`#${FILTERS_ID}`}>Change the range</a>
        </p>
      )}
    </Card>
  );
}

/** The legend: a swatch and the name of each series. */
function Legend({ spec }: { spec: ChartSpec }) {
  return (
    <ul className="sk-chart__legend" aria-label="Series">
      {spec.series.map((s) => (
        <li key={s.key}>
          <svg width="12" height="12" aria-hidden="true">
            <rect width="12" height="12" rx="2" fill={s.color} />
          </svg>
          {s.label}
        </li>
      ))}
    </ul>
  );
}

/** The numbers of a chart as a table, one row for each point. */
function ChartTable({ title, spec }: { title: string; spec: ChartSpec }) {
  const captionId = useId();
  const columns = spec.tableColumns ?? spec.series.map((s) => ({ key: s.key, label: s.label }));

  return (
    <div className="sk-chart__table-wrap" role="region" aria-labelledby={captionId} tabIndex={0}>
      <table className="sk-chart__table">
        <caption id={captionId}>{title}, as a table</caption>
        <thead>
          <tr>
            <th scope="col">{spec.xLabel}</th>
            {columns.map((c) => (
              <th key={c.key} scope="col">
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {spec.data.map((row) => (
            <tr key={row.x}>
              <th scope="row">{row.x}</th>
              {columns.map((c) => (
                <td key={c.key}>{typeof row[c.key] === "number" ? spec.format(row[c.key] as number) : "none"}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
