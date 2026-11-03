import type { BenchRunSummary } from "../api/bench";

const HOUR_MS = 3_600_000;
const NEWEST = Date.UTC(2026, 9, 28, 12);

/** The ID of the run with number n. */
export function benchRunId(n: number): string {
  return `0192d3a4-0000-7000-8000-${n.toString(16).padStart(12, "0")}`;
}

/** A benchmark run. Run 1 is the newest, and every run is an hour older than the one before. Odd runs pass and even runs fail. */
export function benchRun(n: number, overrides: Partial<BenchRunSummary> = {}): BenchRunSummary {
  const startedAt = NEWEST - n * HOUR_MS;

  return {
    id: benchRunId(n),
    startedAt: new Date(startedAt).toISOString(),
    finishedAt: new Date(startedAt + HOUR_MS / 2).toISOString(),
    sidekickVersion: "v0.9.1",
    gitSha: "abc1234",
    connectors: { investigate: { connector: "claude", model: "big-model" } },
    strategies: {},
    scenarioSet: "starter",
    repeats: 2,
    status: n % 2 === 1 ? "passed" : "failed",
    triggeredBy: "cli",
    metrics: { runs: 4, toolCalls: 12, inputTokens: 1000, outputTokens: 500, costMicros: 8000, judgeCostMicros: 100, judgeErrors: 0, overall: 0.7, verdictAccuracy: 0.75, meanCostPerRunUsd: 0.002, meanDurationMs: 1500 },
    ...overrides,
  };
}

/** The runs from number first to number last, both included. */
export function benchRunRange(first: number, last: number): BenchRunSummary[] {
  return Array.from({ length: last - first + 1 }, (_, i) => benchRun(first + i));
}
