import type { BenchResult } from "../../api/bench";

/** How a scenario did: no repeat right, some right, or every repeat right. */
export type ScenarioState = "wrong" | "partly" | "right";

/** How many repeats of a scenario gave one verdict; verdict is undefined for repeats that gave none. */
export interface VerdictCount {
  verdict: string | undefined;
  count: number;
}

/** The repeats of one scenario with their totals. */
export interface ScenarioGroup {
  scenarioId: string;
  expectedVerdict: string;
  repeats: BenchResult[];
  correct: number;
  total: number;
  state: ScenarioState;
  verdicts: VerdictCount[];
  /** The mean root cause score of the repeats that were graded; undefined when none was. */
  meanRootCauseScore: number | undefined;
  meanCostMicros: number;
  meanDurationMs: number;
}

const STATE_ORDER: Record<ScenarioState, number> = { wrong: 0, partly: 1, right: 2 };

/**
 * The flat results of a run, grouped by scenario. A scenario with no right
 * repeat comes first, then one with some, then one with all; inside each, the
 * lowest share of right repeats first, then the scenario ID.
 */
export function groupScenarios(results: readonly BenchResult[]): ScenarioGroup[] {
  const byScenario = new Map<string, BenchResult[]>();

  for (const result of results) {
    byScenario.set(result.scenarioId, [...(byScenario.get(result.scenarioId) ?? []), result]);
  }

  return [...byScenario.entries()].map(([scenarioId, repeats]) => groupOf(scenarioId, repeats)).sort(compareGroups);
}

function groupOf(scenarioId: string, unsorted: BenchResult[]): ScenarioGroup {
  const repeats = [...unsorted].sort((a, b) => a.repeat - b.repeat);
  const correct = repeats.filter((r) => r.verdictCorrect).length;
  const scores = repeats.flatMap((r) => (r.rootCauseScore === undefined ? [] : [r.rootCauseScore]));

  return {
    scenarioId,
    expectedVerdict: repeats[0]?.expectedVerdict ?? "",
    repeats,
    correct,
    total: repeats.length,
    state: correct === 0 ? "wrong" : correct === repeats.length ? "right" : "partly",
    verdicts: verdictCounts(repeats),
    meanRootCauseScore: scores.length === 0 ? undefined : mean(scores),
    meanCostMicros: mean(repeats.map((r) => r.costMicros)),
    meanDurationMs: mean(repeats.map((r) => r.durationMs)),
  };
}

function verdictCounts(repeats: readonly BenchResult[]): VerdictCount[] {
  const counts = new Map<string | undefined, number>();

  for (const r of repeats) {
    counts.set(r.verdict, (counts.get(r.verdict) ?? 0) + 1);
  }

  return [...counts.entries()].map(([verdict, count]) => ({ verdict, count }));
}

function compareGroups(a: ScenarioGroup, b: ScenarioGroup): number {
  return STATE_ORDER[a.state] - STATE_ORDER[b.state] || a.correct / a.total - b.correct / b.total || a.scenarioId.localeCompare(b.scenarioId);
}

function mean(values: readonly number[]): number {
  return values.reduce((sum, v) => sum + v, 0) / values.length;
}
