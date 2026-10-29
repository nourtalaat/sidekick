import type { BenchComparison, BenchRunSummary, CostStats, OperationsStats, QualityStats, StatsFilters } from "../api/stats";

/** The first and last day of the ranges the fixtures answer for, as a request without a range gets them. */
export const FROM = "2026-09-29";
export const TO = "2026-10-28";

export const FILTER_OPTIONS: StatsFilters = { routes: ["payments", "search"], services: ["checkout-api", "search-api"], models: ["big-model", "small-model"] };

/** Quality numbers with two days of ratings. */
export function quality(overrides: Partial<QualityStats> = {}): QualityStats {
  return {
    from: FROM,
    to: TO,
    totals: { rated: 17, correct: 13, accuracy: 13 / 17 },
    previous: { rated: 4, correct: 3, accuracy: 0.75 },
    days: [
      { day: "2026-10-20", rated: 13, correct: 11 },
      { day: "2026-10-21", rated: 4, correct: 2 },
    ],
    matrix: [
      { predicted: "false_positive", confirmed: "false_positive", n: 8 },
      { predicted: "false_positive", confirmed: "true_positive", n: 2 },
      { predicted: "false_positive", confirmed: "unknown", n: 1 },
      { predicted: "true_positive", confirmed: "false_positive", n: 1 },
      { predicted: "true_positive", confirmed: "true_positive", n: 5 },
    ],
    perVerdict: [
      { verdict: "true_positive", predicted: 6, actual: 7, hits: 5, precision: 5 / 6, recall: 5 / 7 },
      { verdict: "false_positive", predicted: 10, actual: 9, hits: 8, precision: 0.8, recall: 8 / 9 },
    ],
    comparison: [
      { model: "big-model", strategy: "generic-alert", rated: 14, correct: 11 },
      { model: "small-model", strategy: "deep", rated: 3, correct: 2 },
    ],
    ...overrides,
  };
}

/** The same ranges with nothing rated. */
export function emptyQuality(): QualityStats {
  return quality({
    totals: { rated: 0, correct: 0 },
    previous: { rated: 0, correct: 0 },
    days: [],
    matrix: [],
    perVerdict: [],
    comparison: [],
  });
}

/** Spend over three days, with a daily cap of $0.10 that today's spend is near. */
export function cost(overrides: Partial<CostStats> = {}): CostStats {
  return {
    from: FROM,
    to: TO,
    ignoredFilters: [],
    totals: { costMicros: 106_000, inputTokens: 44_000, outputTokens: 22_000 },
    previous: { costMicros: 10_000, inputTokens: 5_000, outputTokens: 2_500 },
    days: [
      { day: "2026-10-20", costMicros: 25_000, inputTokens: 12_000, outputTokens: 6_000 },
      { day: "2026-10-27", costMicros: 16_000, inputTokens: 7_000, outputTokens: 3_500 },
      { day: "2026-10-28", costMicros: 65_000, inputTokens: 25_000, outputTokens: 12_500 },
    ],
    dayModels: [
      { day: "2026-10-20", connector: "claude", model: "big-model", costMicros: 20_000, inputTokens: 10_000, outputTokens: 5_000 },
      { day: "2026-10-20", connector: "claude", model: "small-model", costMicros: 5_000, inputTokens: 2_000, outputTokens: 1_000 },
      { day: "2026-10-27", connector: "claude", model: "big-model", costMicros: 16_000, inputTokens: 7_000, outputTokens: 3_500 },
      { day: "2026-10-28", connector: "claude", model: "big-model", costMicros: 40_000, inputTokens: 16_000, outputTokens: 8_000 },
      { day: "2026-10-28", connector: "claude", model: "small-model", costMicros: 25_000, inputTokens: 9_000, outputTokens: 4_500 },
    ],
    byModel: [
      { connector: "claude", model: "big-model", costMicros: 76_000, inputTokens: 33_000, outputTokens: 16_500 },
      { connector: "claude", model: "small-model", costMicros: 30_000, inputTokens: 11_000, outputTokens: 5_500 },
    ],
    byRoute: [
      { route: "payments", costMicros: 76_000, inputTokens: 33_000, outputTokens: 16_500 },
      { route: "search", costMicros: 30_000, inputTokens: 11_000, outputTokens: 5_500 },
    ],
    perInvestigation: [
      { day: "2026-10-20", investigations: 14, costMicros: 40_000 },
      { day: "2026-10-21", investigations: 8, costMicros: 7_000 },
    ],
    previousPerInvestigation: { investigations: 5, costMicros: 10_000 },
    today: {
      day: "2026-10-28",
      spentMicros: 85_000,
      capMicros: 100_000,
      routes: [
        { route: "payments", spentMicros: 60_000, capMicros: 50_000 },
        { route: "search", spentMicros: 25_000 },
      ],
    },
    ...overrides,
  };
}

/** The same ranges with nothing spent. */
export function emptyCost(): CostStats {
  return cost({
    totals: { costMicros: 0, inputTokens: 0, outputTokens: 0 },
    previous: { costMicros: 0, inputTokens: 0, outputTokens: 0 },
    days: [],
    dayModels: [],
    byModel: [],
    byRoute: [],
    perInvestigation: [],
    previousPerInvestigation: { investigations: 0, costMicros: 0 },
    today: { day: "2026-10-28", spentMicros: 0, routes: [] },
  });
}

const figures = {
  durationN: 0,
  durationMsSum: 0,
  firstFindingN: 0,
  firstFindingMsSum: 0,
  toolCalls: 0,
  noisyFixed: 0,
  actionsProposed: 0,
  actionsApproved: 0,
  actionsExecuted: 0,
  recoveries: 0,
  downtimeMsSum: 0,
  watchTimeouts: 0,
};

/** Operations over two days. */
export function operations(overrides: Partial<OperationsStats> = {}): OperationsStats {
  return {
    from: FROM,
    to: TO,
    interval: "day",
    series: [
      {
        ...figures,
        bucket: "2026-10-20T00:00:00Z",
        investigations: 14,
        finished: 14,
        durationN: 14,
        durationMsSum: 2_400_000,
        firstFindingN: 12,
        firstFindingMsSum: 520_000,
        toolCalls: 90,
        noisyFixed: 2,
        recoveries: 2,
        downtimeMsSum: 1_440_000,
        watchTimeouts: 1,
        verdicts: [
          { verdict: "false_positive", investigations: 10 },
          { verdict: "true_positive", investigations: 4 },
        ],
      },
      {
        ...figures,
        bucket: "2026-10-21T00:00:00Z",
        investigations: 9,
        finished: 8,
        durationN: 8,
        durationMsSum: 460_000,
        firstFindingN: 6,
        firstFindingMsSum: 360_000,
        toolCalls: 14,
        noisyFixed: 1,
        verdicts: [
          { verdict: "duplicate", investigations: 2 },
          { verdict: "known_issue", investigations: 1 },
          { verdict: "false_positive", investigations: 4 },
          { verdict: "inconclusive", investigations: 1 },
          { verdict: "none", investigations: 1 },
        ],
      },
    ],
    totals: {
      ...figures,
      investigations: 23,
      finished: 22,
      durationN: 22,
      durationMsSum: 2_860_000,
      firstFindingN: 18,
      firstFindingMsSum: 880_000,
      toolCalls: 104,
      noisyFixed: 3,
      recoveries: 3,
      downtimeMsSum: 2_040_000,
      watchTimeouts: 1,
      medianFirstFindingMs: 47_244,
      medianDowntimeMs: 749_398,
    },
    previous: { ...figures, investigations: 5, finished: 5, durationN: 5, firstFindingN: 5, firstFindingMsSum: 150_000, medianFirstFindingMs: 27_917 },
    verdicts: [
      { verdict: "true_positive", investigations: 4 },
      { verdict: "false_positive", investigations: 14 },
      { verdict: "inconclusive", investigations: 1 },
      { verdict: "duplicate", investigations: 2 },
      { verdict: "known_issue", investigations: 1 },
      { verdict: "none", investigations: 1 },
    ],
    previousVerdicts: [{ verdict: "false_positive", investigations: 5 }],
    downtime: [
      { route: "payments", service: "checkout-api", recoveries: 2, downtimeMsSum: 1_440_000, watchTimeouts: 1, medianDowntimeMs: 749_398 },
      { route: "search", service: "", recoveries: 1, downtimeMsSum: 600_000, watchTimeouts: 0, medianDowntimeMs: 749_398 },
    ],
    ...overrides,
  };
}

/** The same ranges with no investigations. */
export function emptyOperations(): OperationsStats {
  return operations({
    series: [],
    totals: { ...figures, investigations: 0, finished: 0 },
    previous: { ...figures, investigations: 0, finished: 0 },
    verdicts: [],
    previousVerdicts: [],
    downtime: [],
  });
}

const metrics = { runs: 4, toolCalls: 12, inputTokens: 1000, outputTokens: 500, costMicros: 8000, judgeCostMicros: 100, judgeErrors: 0 };

export const RUN_A_ID = "0192d3a4-0000-7000-8000-0000000000a1";
export const RUN_B_ID = "0192d3a4-0000-7000-8000-0000000000a2";

/** Two benchmark runs, the newest first. */
export function benchRuns(): BenchRunSummary[] {
  return [
    {
      id: RUN_B_ID,
      startedAt: "2026-10-22T10:00:00Z",
      finishedAt: "2026-10-22T10:10:00Z",
      sidekickVersion: "v0.9.1",
      gitSha: "def5678",
      connectors: { investigate: { connector: "openai", model: "small-model" } },
      strategies: {},
      scenarioSet: "starter",
      repeats: 1,
      status: "failed",
      triggeredBy: "ci",
      metrics: { ...metrics, runs: 2, overall: 0.4, verdictAccuracy: 0.5, meanCostPerRunUsd: 0.004, meanDurationMs: 2500 },
    },
    {
      id: RUN_A_ID,
      label: "baseline",
      startedAt: "2026-10-20T10:00:00Z",
      finishedAt: "2026-10-20T10:30:00Z",
      sidekickVersion: "v0.9.0",
      gitSha: "abc1234",
      connectors: { investigate: { connector: "claude", provider: "anthropic", model: "big-model" } },
      strategies: { "generic-alert": "v1" },
      scenarioSet: "starter",
      repeats: 2,
      status: "passed",
      triggeredBy: "cli",
      metrics: { ...metrics, overall: 0.7, verdictAccuracy: 0.75, meanCostPerRunUsd: 0.002, meanDurationMs: 1500 },
    },
  ];
}

/** The comparison of the two runs of benchRuns, run A first. */
export function benchComparison(): BenchComparison {
  const [b, a] = benchRuns();

  return {
    a: a as BenchRunSummary,
    b: b as BenchRunSummary,
    metrics: [
      { key: "overall", label: "overall score", a: 0.7, b: 0.4, change: -0.3, difference: "clear" },
      { key: "verdictAccuracy", label: "verdict accuracy", a: 0.75, b: 0.5, change: -0.25, difference: "none" },
      { key: "tpRecall", label: "true positive recall", a: 0.5, difference: "unknown" },
      { key: "meanCostPerRunUSD", label: "mean cost per run", a: 0.002, b: 0.004, change: 0.002, difference: "unknown" },
      { key: "meanDurationMs", label: "mean time per run", a: 1500, b: 2500, change: 1000, difference: "unknown" },
    ],
    scenarios: [
      { scenarioId: "s1", expectedVerdict: "false_positive", a: { runs: 2, correct: 2, rootCauseMean: 0.85 }, b: { runs: 1, correct: 0 } },
      { scenarioId: "s2", expectedVerdict: "true_positive", a: { runs: 2, correct: 1 } },
      { scenarioId: "s3", expectedVerdict: "true_positive", b: { runs: 1, correct: 1 } },
    ],
  };
}
