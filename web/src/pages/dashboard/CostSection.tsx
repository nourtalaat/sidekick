import { useId } from "react";
import type { UseQueryResult } from "@tanstack/react-query";
import type { CostStats } from "../../api/stats";
import { StatusBadge } from "../../components/Badge";
import { Card } from "../../components/Card";
import { Table } from "../../components/Table";
import { formatCost, formatTokens } from "../../format";
import { ChartCard } from "./ChartCard";
import { categoryColor, type ChartSpec } from "./chart";
import { costPerInvestigationRows, routeRows, spendByModel } from "./data";
import { Loaded } from "./Loaded";
import { capUsed } from "./words";
import "./Dashboard.css";

/** The share of a cap from which the page says the spend is near it. */
const NEAR_CAP = 0.8;

interface CostSectionProps {
  cost: UseQueryResult<CostStats>;
  /** Every UTC day of the range, as YYYY-MM-DD. */
  days: string[];
}

/** Cost: spend per day by model against the daily cap, spend by route and today's spend by route, tokens, and the cost of an investigation. */
export function CostSection({ cost, days }: CostSectionProps) {
  const headingId = useId();

  return (
    <section aria-labelledby={headingId} className="sk-dashboard__section">
      <h2 id={headingId} className="sk-dashboard__heading">
        Cost
      </h2>
      <Loaded result={cost}>
        {(stats) => {
          const spend = spendByModel(stats, days);
          const note = notApplied(stats);

          return (
            <>
              {note && <p className="sk-dashboard__note">{note}</p>}
              <div className="sk-dashboard__grid">
                <ChartCard
                  title="Spend per day by model"
                  summary={`From ${stats.from} to ${stats.to}, model calls cost ${formatCost(stats.totals.costMicros)}, with ${formatTokens(stats.totals.inputTokens)} tokens in and ${formatTokens(stats.totals.outputTokens)} out.`}
                  spec={{
                    kind: "stack",
                    data: spend.rows,
                    series: spend.series,
                    xLabel: "Day (UTC)",
                    format: formatCost,
                    referenceLine: stats.today.capMicros ? { value: stats.today.capMicros, label: `Daily cap ${formatCost(stats.today.capMicros)}` } : undefined,
                    domain: [0, ceiling(stats, spend.rows.map((r) => spend.series.reduce((sum, s) => sum + Number(r[s.key] ?? 0), 0)))],
                  }}
                  aside={<CapStatus today={stats.today} />}
                />
                <TodayByRoute today={stats.today} />
                <ChartCard
                  title="Spend by route"
                  summary={`Spend of ${stats.byRoute.length} routes from ${stats.from} to ${stats.to}.`}
                  spec={routeSpec(stats)}
                />
                <ChartCard
                  title="Cost per investigation"
                  summary={`The mean cost of an investigation by the day it was created, from ${stats.from} to ${stats.to}.`}
                  spec={perInvestigationSpec(stats, days)}
                />
                <ChartCard title="Tokens in per day" summary={`Input tokens of model calls per day, ${formatTokens(stats.totals.inputTokens)} in all.`} spec={tokenSpec(spend.tokens.in, "Tokens in", 0)} />
                <ChartCard title="Tokens out per day" summary={`Output tokens of model calls per day, ${formatTokens(stats.totals.outputTokens)} in all.`} spec={tokenSpec(spend.tokens.out, "Tokens out", 1)} />
              </div>
            </>
          );
        }}
      </Loaded>
    </section>
  );
}

/** The top of the spend axis: the biggest day, or the cap when that is more, so the cap line is in view. */
function ceiling(stats: CostStats, dayTotals: number[]): number {
  return Math.max(stats.today.capMicros ?? 0, ...dayTotals, 1);
}

/** Says which filters the spend could not use, in words; nothing when it used them all. */
function notApplied(stats: CostStats): string | undefined {
  if (stats.ignoredFilters.length === 0) {
    return undefined;
  }

  return `Spend, tokens and today's figures cannot be filtered by ${stats.ignoredFilters.join(" or ")}; only the cost per investigation uses ${stats.ignoredFilters.length === 1 ? "it" : "them"}.`;
}

function routeSpec(stats: CostStats): ChartSpec {
  return {
    kind: "bar",
    data: routeRows(stats),
    series: [{ key: "spend", label: "Spend", color: categoryColor(0) }],
    xLabel: "Route",
    format: formatCost,
  };
}

function perInvestigationSpec(stats: CostStats, days: string[]): ChartSpec {
  return {
    kind: "line",
    data: costPerInvestigationRows(stats, days),
    series: [{ key: "cost", label: "Cost per investigation", color: categoryColor(0) }],
    xLabel: "Day (UTC)",
    format: formatCost,
    tableColumns: [
      { key: "cost", label: "Cost per investigation" },
      { key: "investigations", label: "Investigations" },
    ],
  };
}

function tokenSpec(rows: ChartSpec["data"], label: string, slot: number): ChartSpec {
  return { kind: "bar", data: rows, series: [{ key: "tokens", label, color: categoryColor(slot) }], xLabel: "Day (UTC)", format: formatTokens };
}

type Today = CostStats["today"];

/** A status next to the daily cap when today's spend is near it or over it. */
function CapStatus({ today }: { today: Today }) {
  if (!today.capMicros) {
    return null;
  }

  const share = today.spentMicros / today.capMicros;

  return (
    <p className="sk-dashboard__cap">
      {share >= 1 ? <StatusBadge status="danger">Over the daily cap</StatusBadge> : share >= NEAR_CAP ? <StatusBadge status="warning">Near daily cap</StatusBadge> : null}
      <span>
        {`${formatCost(today.spentMicros)} of ${formatCost(today.capMicros)} spent today, ${capUsed(today.spentMicros, today.capMicros)}`}
      </span>
    </p>
  );
}

/** What each route spent today against its cap, if it has one. */
function TodayByRoute({ today }: { today: Today }) {
  return (
    <Card title="Spend today by route" level={3}>
      {today.routes.length === 0 ? (
        <p className="sk-chart__empty">Nothing spent yet today.</p>
      ) : (
        <Table label="Spend and cap of each route today" className="sk-numbers">
          <thead>
            <tr>
              <th scope="col">Route</th>
              <th scope="col">Spent</th>
              <th scope="col">Daily cap</th>
              <th scope="col">Status</th>
            </tr>
          </thead>
          <tbody>
            {today.routes.map((r) => (
              <tr key={r.route}>
                <th scope="row">{r.route}</th>
                <td>{formatCost(r.spentMicros)}</td>
                <td>{r.capMicros ? formatCost(r.capMicros) : "no cap"}</td>
                <td>{r.capMicros ? <RouteStatus spent={r.spentMicros} cap={r.capMicros} /> : "No cap set"}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Card>
  );
}

function RouteStatus({ spent, cap }: { spent: number; cap: number }) {
  const share = spent / cap;

  if (share >= 1) {
    return <StatusBadge status="danger">Over the daily cap</StatusBadge>;
  }

  if (share >= NEAR_CAP) {
    return <StatusBadge status="warning">Near daily cap</StatusBadge>;
  }

  return <StatusBadge status="success">{capUsed(spent, cap)}</StatusBadge>;
}
