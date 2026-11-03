import type { BenchInterval, BenchRun } from "../../api/bench";
import { formatShare } from "../dashboard/words";

/** The keys of the share metrics, as `metrics` and `intervals` of a run name them. */
export type ShareKey =
  | "overall"
  | "verdictAccuracy"
  | "fpPrecision"
  | "fpRecall"
  | "tpPrecision"
  | "tpRecall"
  | "rootCauseMatch"
  | "rootCauseScore"
  | "evidence"
  | "proposalCorrect"
  | "sourceFailuresReported";

/** The share metrics of a run, in the order the page shows them. A new metric is one line here. */
export const METRIC_ROWS: readonly { key: ShareKey; label: string }[] = [
  { key: "overall", label: "Overall score" },
  { key: "verdictAccuracy", label: "Verdict accuracy" },
  { key: "fpPrecision", label: "False positive precision" },
  { key: "fpRecall", label: "False positive recall" },
  { key: "tpPrecision", label: "True positive precision" },
  { key: "tpRecall", label: "True positive recall" },
  { key: "rootCauseMatch", label: "Root cause match" },
  { key: "rootCauseScore", label: "Root cause score" },
  { key: "evidence", label: "Evidence" },
  { key: "proposalCorrect", label: "Proposals correct" },
  { key: "sourceFailuresReported", label: "Source failures reported" },
];

/** One share metric of a run: its value, and its interval when the run has one. */
export interface ScoreRow {
  key: ShareKey;
  label: string;
  value: number | undefined;
  interval: BenchInterval | undefined;
}

/** The share metrics of a run, one row for each entry of METRIC_ROWS. */
export function metricScoreRows(metrics: BenchRun["metrics"], intervals: BenchRun["intervals"]): ScoreRow[] {
  return METRIC_ROWS.map(({ key, label }) => ({ key, label, value: metrics[key], interval: intervals[key] }));
}

/** An interval as "62% to 88%", or "none" when the metric has no interval. */
export function intervalText(interval: BenchInterval | undefined): string {
  return interval === undefined ? "none" : `${formatShare(interval.lower)} to ${formatShare(interval.upper)}`;
}
