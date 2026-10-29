import type { CostStats, OperationsStats, QualityStats } from "../../api/stats";
import { CATEGORY_COLORS, categoryColor, OTHER_COLOR, type ChartRow, type ChartSeries } from "./chart";

const MS_PER_DAY = 86_400_000;
const PERCENT = 100;

/** The key of the series that the models past the palette fold into. */
const OTHER_KEY = "other models";

/** How many models get a color of their own in the spend chart; the rest fold into "Other". */
export const MODEL_SERIES_LIMIT = CATEGORY_COLORS.length - 1;

/** A UTC date such as "2026-10-20" as "Oct 20". */
export function dayLabel(date: string): string {
  return new Date(`${date}T00:00:00Z`).toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
}

/** The start of a UTC hour as "Oct 20, 10:00 UTC". */
export function hourLabel(iso: string): string {
  const time = new Date(iso);

  return `${dayLabel(iso.slice(0, 10))}, ${String(time.getUTCHours()).padStart(2, "0")}:00 UTC`;
}

/** Every UTC date from `from` to `to`, both included, as YYYY-MM-DD. */
export function eachDay(from: string, to: string): string[] {
  const days: string[] = [];
  const end = Date.parse(`${to}T00:00:00Z`);

  for (let t = Date.parse(`${from}T00:00:00Z`); t <= end; t += MS_PER_DAY) {
    days.push(new Date(t).toISOString().slice(0, 10));
  }

  return days;
}

/** One chart row for each of the days, with the values of that day or, for a day with none, the zero values. */
export function dayRows(days: string[], values: Map<string, Record<string, number | undefined>>, zero: Record<string, number | undefined>): ChartRow[] {
  return days.map((day) => ({ x: dayLabel(day), ...(values.get(day) ?? zero) }));
}

/** The accuracy of the verdicts by day, in percent, with the counts for the table. */
export function accuracyRows(stats: QualityStats, days: string[]): ChartRow[] {
  const byDay = new Map(stats.days.map((d) => [d.day, { accuracy: d.rated === 0 ? undefined : (d.correct / d.rated) * PERCENT, rated: d.rated, correct: d.correct }]));

  return dayRows(days, byDay, { accuracy: undefined, rated: 0, correct: 0 });
}

/** The accuracy of each model and strategy, in percent, for a bar chart. */
export function comparisonRows(stats: QualityStats): ChartRow[] {
  return stats.comparison.map((c) => ({
    x: [c.model || "no model", c.strategy || "no strategy"].join(" / "),
    accuracy: c.rated === 0 ? undefined : (c.correct / c.rated) * PERCENT,
    rated: c.rated,
    correct: c.correct,
  }));
}

/** What the spend by model chart draws: the series with their colors and the row of each day. */
export interface SpendByModel {
  series: ChartSeries[];
  rows: ChartRow[];
  tokens: { in: ChartRow[]; out: ChartRow[] };
}

/**
 * The spend of each day by model, stacked. The models with the most spend get
 * the palette in order; past the palette's limit the rest fold into Other.
 */
export function spendByModel(stats: CostStats, days: string[]): SpendByModel {
  const total = new Map<string, number>();
  for (const row of stats.dayModels) {
    total.set(row.model, (total.get(row.model) ?? 0) + row.costMicros);
  }

  const ranked = [...total.entries()].sort((a, b) => b[1] - a[1]).map(([model]) => model);
  const named = ranked.length > CATEGORY_COLORS.length ? ranked.slice(0, MODEL_SERIES_LIMIT) : ranked;
  const folded = ranked.length > named.length;

  const series: ChartSeries[] = named.map((model, i) => ({ key: model, label: model || "no model", color: categoryColor(i) }));
  if (folded) {
    series.push({ key: OTHER_KEY, label: "Other", color: OTHER_COLOR });
  }

  const byDay = new Map<string, Record<string, number>>();
  for (const row of stats.dayModels) {
    const values = byDay.get(row.day) ?? {};
    const key = named.includes(row.model) ? row.model : OTHER_KEY;
    values[key] = (values[key] ?? 0) + row.costMicros;
    byDay.set(row.day, values);
  }

  const zero = Object.fromEntries(series.map((s) => [s.key, 0]));

  return {
    series,
    rows: dayRows(days, byDay, zero),
    tokens: {
      in: dayRows(days, new Map(stats.days.map((d) => [d.day, { tokens: d.inputTokens }])), { tokens: 0 }),
      out: dayRows(days, new Map(stats.days.map((d) => [d.day, { tokens: d.outputTokens }])), { tokens: 0 }),
    },
  };
}

/** What the cost of an investigation was on each day: the mean, for a line. */
export function costPerInvestigationRows(stats: CostStats, days: string[]): ChartRow[] {
  const byDay = new Map(stats.perInvestigation.map((d) => [d.day, { cost: d.investigations === 0 ? undefined : d.costMicros / d.investigations, investigations: d.investigations }]));

  return dayRows(days, byDay, { cost: undefined, investigations: 0 });
}

/** The spend spent on each route, for a bar chart. */
export function routeRows(stats: CostStats): ChartRow[] {
  return stats.byRoute.map((r) => ({ x: r.route, spend: r.costMicros }));
}

/** The rows of a series by bucket, with a value computed from each bucket. */
function bucketRows(stats: OperationsStats, days: string[], value: (b: OperationsStats["series"][number]) => Record<string, number | undefined>, zero: Record<string, number | undefined>): ChartRow[] {
  if (stats.interval === "hour") {
    return stats.series.map((b) => ({ x: hourLabel(b.bucket), ...value(b) }));
  }

  return dayRows(days, new Map(stats.series.map((b) => [b.bucket.slice(0, 10), value(b)])), zero);
}

/** The investigations of each bucket by verdict, with Duplicate and Known issue added up for the chart. */
export function verdictRows(stats: OperationsStats, days: string[]): ChartRow[] {
  const zero = { true_positive: 0, flapping: 0, duplicate: 0, known_issue: 0, duplicate_or_known: 0, false_positive: 0, inconclusive: 0, none: 0 };

  return bucketRows(
    stats,
    days,
    (b) => {
      const counts = { ...zero };
      for (const v of b.verdicts) {
        counts[v.verdict as keyof typeof zero] = v.investigations;
      }

      return { ...counts, duplicate_or_known: counts.duplicate + counts.known_issue };
    },
    zero,
  );
}

/** The mean time to the first finding of each bucket, in milliseconds. */
export function firstFindingRows(stats: OperationsStats, days: string[]): ChartRow[] {
  return bucketRows(stats, days, (b) => ({ time: b.firstFindingN === 0 ? undefined : b.firstFindingMsSum / b.firstFindingN, n: b.firstFindingN }), { time: undefined, n: 0 });
}

/** The mean duration of an investigation of each bucket, in milliseconds. */
export function durationRows(stats: OperationsStats, days: string[]): ChartRow[] {
  return bucketRows(stats, days, (b) => ({ time: b.durationN === 0 ? undefined : b.durationMsSum / b.durationN, n: b.durationN }), { time: undefined, n: 0 });
}

/** The mean number of tool calls of an investigation of each bucket. */
export function toolCallRows(stats: OperationsStats, days: string[]): ChartRow[] {
  return bucketRows(stats, days, (b) => ({ calls: b.investigations === 0 ? undefined : b.toolCalls / b.investigations, investigations: b.investigations }), { calls: undefined, investigations: 0 });
}

/** The false alarms of each bucket: investigations that ended with the verdict false positive. */
export function falseAlarmRows(stats: OperationsStats, days: string[]): ChartRow[] {
  return bucketRows(stats, days, (b) => ({ count: b.verdicts.find((v) => v.verdict === "false_positive")?.investigations ?? 0 }), { count: 0 });
}

/** The noisy alerts a responder marked as fixed, by bucket. */
export function noisyFixedRows(stats: OperationsStats, days: string[]): ChartRow[] {
  return bucketRows(stats, days, (b) => ({ count: b.noisyFixed }), { count: 0 });
}

/** The actions proposed, approved and taken, by bucket. */
export function actionRows(stats: OperationsStats, days: string[]): ChartRow[] {
  return bucketRows(
    stats,
    days,
    (b) => ({ proposed: b.actionsProposed, approved: b.actionsApproved, taken: b.actionsExecuted }),
    { proposed: 0, approved: 0, taken: 0 },
  );
}

/** The count of investigations with a verdict in a list of counts by verdict. */
export function countOf(verdicts: { verdict: string; investigations: number }[], verdict: string): number {
  return verdicts.find((v) => v.verdict === verdict)?.investigations ?? 0;
}
