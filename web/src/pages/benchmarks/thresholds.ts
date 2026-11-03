import type { BenchThreshold } from "../../api/bench";
import { formatCost, MICROS_PER_DOLLAR } from "../../format";

const PERCENT = 100;
const COST_THRESHOLD = "maxCostPerRunUSD";

const NAMES: Record<string, string> = {
  verdictAccuracy: "Verdict accuracy",
  fpPrecision: "False positive precision",
  rootCauseMatch: "Root cause match",
  proposalCorrect: "Proposals correct",
  [COST_THRESHOLD]: "Mean cost per run",
  baseline: "Overall score against the baseline",
};

/** A threshold in words; a name this page does not know shows as the API sent it. */
export function thresholdName(name: string): string {
  return NAMES[name] ?? name;
}

/** The limit of a threshold with its direction, such as "at least 80%" or "at most $0.0010". */
export function thresholdLimitText(t: BenchThreshold): string {
  return `${t.name === COST_THRESHOLD ? "at most" : "at least"} ${amountText(t, t.limit)}`;
}

/** The run's value of a threshold, such as "72.5%"; "none yet" when the run had nothing to count. */
export function thresholdValueText(t: BenchThreshold): string {
  return t.value === undefined ? "none yet" : amountText(t, t.value);
}

/** The thresholds that failed, in the order the run checked them. */
export function failedThresholds(thresholds: readonly BenchThreshold[]): BenchThreshold[] {
  return thresholds.filter((t) => !t.passed);
}

/** A share as a percentage with up to one decimal, so a miss never reads as equal to its limit. */
function percentText(share: number): string {
  return `${Number((share * PERCENT).toFixed(1))}%`;
}

function amountText(t: BenchThreshold, amount: number): string {
  return t.name === COST_THRESHOLD ? formatCost(amount * MICROS_PER_DOLLAR) : percentText(amount);
}
