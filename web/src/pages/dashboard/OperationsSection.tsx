import { useId } from "react";
import type { UseQueryResult } from "@tanstack/react-query";
import type { OperationsStats } from "../../api/stats";
import { Card } from "../../components/Card";
import { Table } from "../../components/Table";
import { formatDuration } from "../../format";
import { ChartCard, FILTERS_ID } from "./ChartCard";
import { categoryColor, VERDICT_SERIES, type ChartSpec } from "./chart";
import { actionRows, durationRows, falseAlarmRows, firstFindingRows, noisyFixedRows, toolCallRows, verdictRows } from "./data";
import { Loaded } from "./Loaded";
import { formatCount } from "./words";
import "./Dashboard.css";

interface OperationsSectionProps {
  operations: UseQueryResult<OperationsStats>;
  /** Every UTC day of the range, as YYYY-MM-DD. */
  days: string[];
}

/** Operations: volume by verdict, speed, tool calls, false alarms, fixes, actions and downtime. */
export function OperationsSection({ operations, days }: OperationsSectionProps) {
  const headingId = useId();

  return (
    <section aria-labelledby={headingId} className="sk-dashboard__section">
      <h2 id={headingId} className="sk-dashboard__heading">
        Operations
      </h2>
      <Loaded result={operations}>
        {(stats) => {
          const per = stats.interval === "hour" ? "hour" : "day";
          const xLabel = stats.interval === "hour" ? "Hour (UTC)" : "Day (UTC)";
          const total = stats.totals;

          return (
            <div className="sk-dashboard__grid">
              <ChartCard
                title={`Investigations per ${per} by verdict`}
                summary={`From ${stats.from} to ${stats.to}, ${formatCount(total.investigations)} investigations were created, ${formatCount(total.finished)} of them ended.`}
                spec={{
                  kind: "stack",
                  data: verdictRows(stats, days),
                  series: [...VERDICT_SERIES],
                  xLabel,
                  format: formatCount,
                  tableColumns: [
                    { key: "true_positive", label: "Real issue" },
                    { key: "flapping", label: "Flapping" },
                    { key: "duplicate", label: "Duplicate" },
                    { key: "known_issue", label: "Known issue" },
                    { key: "false_positive", label: "False alarm" },
                    { key: "inconclusive", label: "Not sure yet" },
                    { key: "none", label: "No verdict yet" },
                  ],
                }}
              />
              <ChartCard
                title="Time to first finding"
                summary={`The mean time from creation to the first finding of each ${per}. The median over the range is ${medianText(total.medianFirstFindingMs)}.`}
                spec={timeSpec(firstFindingRows(stats, days), "Mean time to first finding", xLabel)}
              />
              <ChartCard
                title="Duration of an investigation"
                summary={`The mean time an investigation took, by the ${per} it was created. Over the range the mean is ${total.durationN === 0 ? "not known yet" : formatDuration(total.durationMsSum / total.durationN)}.`}
                spec={timeSpec(durationRows(stats, days), "Mean duration", xLabel)}
              />
              <ChartCard
                title="Tool calls per investigation"
                summary={`The mean number of tool calls of an investigation, by the ${per} it was created: ${formatCount(total.toolCalls)} calls in all.`}
                spec={{
                  kind: "line",
                  data: toolCallRows(stats, days),
                  series: [{ key: "calls", label: "Tool calls per investigation", color: categoryColor(0) }],
                  xLabel,
                  format: (n) => n.toFixed(1),
                  tableColumns: [
                    { key: "calls", label: "Tool calls per investigation" },
                    { key: "investigations", label: "Investigations" },
                  ],
                }}
              />
              <ChartCard
                title="False alarms found"
                summary={`Investigations that ended as a false alarm, by ${per}.`}
                spec={countSpec(falseAlarmRows(stats, days), "False alarms", xLabel, "var(--sk-verdict-false-solid)")}
              />
              <ChartCard
                title="Noisy alerts fixed"
                summary={`False alarms whose fix a responder marked as applied, by ${per}: ${formatCount(total.noisyFixed)} in all.`}
                spec={countSpec(noisyFixedRows(stats, days), "Noisy alerts fixed", xLabel, categoryColor(0))}
              />
              <ChartCard
                title="Actions proposed, approved and taken"
                summary={`Actions by ${per}: ${formatCount(total.actionsProposed)} proposed, ${formatCount(total.actionsApproved)} approved, ${formatCount(total.actionsExecuted)} taken.`}
                spec={{
                  kind: "bar",
                  data: actionRows(stats, days),
                  series: [
                    { key: "proposed", label: "Proposed", color: categoryColor(0) },
                    { key: "approved", label: "Approved", color: categoryColor(1) },
                    { key: "taken", label: "Taken", color: categoryColor(2) },
                  ],
                  xLabel,
                  format: formatCount,
                }}
              />
              <Downtime stats={stats} />
            </div>
          );
        }}
      </Loaded>
    </section>
  );
}

function medianText(median: number | undefined): string {
  return median === undefined ? "not known yet" : `about ${formatDuration(median)}`;
}

function timeSpec(data: ChartSpec["data"], label: string, xLabel: string): ChartSpec {
  return {
    kind: "line",
    data,
    series: [{ key: "time", label, color: categoryColor(0) }],
    xLabel,
    format: (ms) => formatDuration(ms),
    tableColumns: [
      { key: "time", label },
      { key: "n", label: "Investigations" },
    ],
  };
}

function countSpec(data: ChartSpec["data"], label: string, xLabel: string, color: string): ChartSpec {
  return { kind: "bar", data, series: [{ key: "count", label, color }], xLabel, format: formatCount };
}

/** Median and total time to recovery per route and service, from the watchers. */
function Downtime({ stats }: { stats: OperationsStats }) {
  return (
    <Card title="Downtime measured by watchers" level={3}>
      {stats.downtime.length === 0 ? (
        <p className="sk-chart__empty">
          No data for this range. <a href={`#${FILTERS_ID}`}>Change the range</a>
        </p>
      ) : (
        <>
          <p className="sk-chart__summary">
            Downtime runs from the first event of an alert to the last recovery of its watchers. A median is about right, not exact.
          </p>
          <Table label="Downtime by route and service" className="sk-numbers">
            <thead>
              <tr>
                <th scope="col">Route</th>
                <th scope="col">Service</th>
                <th scope="col">Recoveries</th>
                <th scope="col">Median downtime</th>
                <th scope="col">Total downtime</th>
                <th scope="col">Watchers timed out</th>
              </tr>
            </thead>
            <tbody>
              {stats.downtime.map((row) => (
                <tr key={`${row.route}/${row.service}`}>
                  <th scope="row">{row.route}</th>
                  <td>{row.service || "no service"}</td>
                  <td>{formatCount(row.recoveries)}</td>
                  <td>{row.medianDowntimeMs === undefined ? "none yet" : `about ${formatDuration(row.medianDowntimeMs)}`}</td>
                  <td>{formatDuration(row.downtimeMsSum)}</td>
                  <td>{formatCount(row.watchTimeouts)}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        </>
      )}
    </Card>
  );
}
