import type { BenchComparison, BenchResult, BenchRun, BenchRunSummary } from "../api/bench";
import { json, problem } from "./helpers";
import { benchComparison, benchRuns, RUN_A_ID, RUN_B_ID } from "./stats";

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

/**
 * A benchmark run with its detail: intervals for every share metric but true
 * positive recall, no proposal score, one passed and two failed thresholds,
 * and the results of three scenarios, one right, one half right and one wrong.
 */
export function benchRunDetail(n: number, overrides: Partial<BenchRun> = {}): BenchRun {
  const interval = (lower: number, upper: number) => ({ lower, upper });

  return {
    ...benchRun(n, { status: "failed" }),
    metrics: {
      ...benchRun(n).metrics,
      fpPrecision: 0.5,
      fpRecall: 1,
      tpPrecision: 1,
      tpRecall: 0.5,
      rootCauseMatch: 0.6,
      rootCauseScore: 0.65,
      evidence: 0.9,
      sourceFailuresReported: 1,
    },
    configHash: "9f2c1d7e5a6b",
    promptVersion: "p-3",
    judgePromptVersion: "j-2",
    intervals: {
      overall: interval(0.62, 0.88),
      verdictAccuracy: interval(0.5, 0.95),
      fpPrecision: interval(0.25, 0.75),
      fpRecall: interval(0.6, 1),
      tpPrecision: interval(0.8, 1),
      rootCauseMatch: interval(0.4, 0.8),
      rootCauseScore: interval(0.45, 0.85),
      evidence: interval(0.7, 1),
      sourceFailuresReported: interval(0.5, 1),
    },
    thresholds: [
      { name: "verdictAccuracy", limit: 0.7, value: 0.75, passed: true },
      { name: "rootCauseMatch", limit: 0.7, value: 0.6, passed: false },
      { name: "maxCostPerRunUSD", limit: 0.001, value: 0.002, passed: false },
      { name: "proposalCorrect", limit: 0.9, passed: true, detail: "nothing to count, so it passes" },
    ],
    results: [
      benchResult("checkout-pool-size", 1, { verdict: "true_positive", expectedVerdict: "true_positive", verdictCorrect: true, rootCauseScore: 1, requiredMet: true, evidenceScore: 1 }),
      benchResult("checkout-pool-size", 2, { verdict: "true_positive", expectedVerdict: "true_positive", verdictCorrect: true, rootCauseScore: 0.5, requiredMet: false, evidenceScore: 0.5 }),
      benchResult("disk-flapping", 1, { verdict: "flapping", expectedVerdict: "flapping", verdictCorrect: true }),
      benchResult("disk-flapping", 2, { verdict: "true_positive", expectedVerdict: "flapping", verdictCorrect: false, rootCauseScore: 0 }),
      benchResult("oom-after-limit", 1, { verdict: "inconclusive", expectedVerdict: "true_positive", verdictCorrect: false, rootCauseScore: 0, requiredMet: false, evidenceScore: 0.25, proposalCorrect: false }),
      benchResult("oom-after-limit", 2, { expectedVerdict: "true_positive", verdictCorrect: false }),
    ],
    ...overrides,
  };
}

/** The result of one repeat of a scenario, with a cost of 2000 micros, 750 ms, 400 input and 100 output tokens and 3 tool calls. */
export function benchResult(scenarioId: string, repeat: number, overrides: Partial<BenchResult> = {}): BenchResult {
  return {
    scenarioId,
    repeat,
    expectedVerdict: "true_positive",
    verdictCorrect: true,
    costMicros: 2000,
    durationMs: 750,
    inputTokens: 400,
    outputTokens: 100,
    toolCalls: 3,
    ...overrides,
  };
}

/** The detail of a run that has the settings of summary, with the prompt versions, thresholds and results of benchRunDetail. */
export function benchRunDetailOf(summary: BenchRunSummary, overrides: Partial<BenchRun> = {}): BenchRun {
  return { ...benchRunDetail(1), ...summary, ...overrides };
}

/** What the compare page's server answers; each part has a default that works. */
export interface CompareAnswers {
  /** The newest runs, as GET /bench/runs answers. */
  runs?: BenchRunSummary[];
  /** Replaces the answer of GET /bench/runs, for a list that fails. */
  list?: () => Response | Promise<Response>;
  /** The answer of GET /bench/compare. */
  compare?: () => Response | Promise<Response>;
  /** The answer of GET /bench/runs/{id}, by ID, for the two runs of benchComparison. */
  details?: Partial<Record<string, () => Response | Promise<Response>>>;
}

/** An answer function for `recording` that serves the list, the comparison and the two run details of benchComparison. */
export function compareAnswers(options: CompareAnswers = {}): (request: { path: string }) => Response | Promise<Response> {
  const comparison: BenchComparison = benchComparison();
  const summaries = { [RUN_A_ID]: comparison.a, [RUN_B_ID]: comparison.b };

  return (request) => {
    if (request.path === "/api/v1/bench/runs") {
      return options.list ? options.list() : json(200, { runs: options.runs ?? benchRuns() });
    }

    if (request.path === "/api/v1/bench/compare") {
      return options.compare ? options.compare() : json(200, comparison);
    }

    for (const [id, summary] of Object.entries(summaries)) {
      if (request.path === `/api/v1/bench/runs/${id}`) {
        return options.details?.[id]?.() ?? json(200, benchRunDetailOf(summary));
      }
    }

    return problem(404, `No route for ${request.path}.`);
  };
}
