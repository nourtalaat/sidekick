import { describe, expect, it } from "vitest";
import { benchResult, benchRunDetail } from "../../test/bench";
import type { BenchScenarioRow } from "../../api/bench";
import { compareScenarios, groupScenarios, scenarioChange } from "./scenarios";

describe("groupScenarios", () => {
  it("groups the flat results and lists wrong scenarios first, then partly wrong, then right", () => {
    const groups = groupScenarios(benchRunDetail(2).results);

    expect(groups.map((g) => [g.scenarioId, g.state, g.correct, g.total])).toEqual([
      ["oom-after-limit", "wrong", 0, 2],
      ["disk-flapping", "partly", 1, 2],
      ["checkout-pool-size", "right", 2, 2],
    ]);
  });

  it("orders inside a state by the share of right repeats, then by scenario ID", () => {
    const wrong = (id: string, repeat: number, verdictCorrect: boolean) => benchResult(id, repeat, { verdictCorrect });
    const groups = groupScenarios([
      wrong("b", 1, true),
      wrong("b", 2, false),
      wrong("b", 3, false),
      wrong("a", 1, true),
      wrong("a", 2, true),
      wrong("a", 3, false),
      wrong("c", 1, true),
      wrong("c", 2, false),
      wrong("c", 3, false),
    ]);

    expect(groups.map((g) => g.scenarioId)).toEqual(["b", "c", "a"]);
  });

  it("sorts the repeats and counts the verdicts given, with the repeats that gave none", () => {
    const [oom] = groupScenarios([benchResult("s", 2, { verdictCorrect: false }), benchResult("s", 1, { verdict: "inconclusive", verdictCorrect: false }), benchResult("s", 3, { verdict: "inconclusive", verdictCorrect: false })]);

    expect(oom?.repeats.map((r) => r.repeat)).toEqual([1, 2, 3]);
    expect(oom?.verdicts).toEqual([
      { verdict: "inconclusive", count: 2 },
      { verdict: undefined, count: 1 },
    ]);
  });

  it("takes the mean root cause score of the graded repeats only, and the mean cost and time of all", () => {
    const [group] = groupScenarios([
      benchResult("s", 1, { rootCauseScore: 1, costMicros: 1000, durationMs: 500 }),
      benchResult("s", 2, { rootCauseScore: 0.5, costMicros: 3000, durationMs: 1500 }),
      benchResult("s", 3, { costMicros: 2000, durationMs: 1000 }),
    ]);

    expect(group?.meanRootCauseScore).toBe(0.75);
    expect(group?.meanCostMicros).toBe(2000);
    expect(group?.meanDurationMs).toBe(1000);
  });

  it("has no mean root cause score when no repeat was graded, and no groups without results", () => {
    expect(groupScenarios([benchResult("s", 1)])[0]?.meanRootCauseScore).toBeUndefined();
    expect(groupScenarios([])).toEqual([]);
  });
});

function row(scenarioId: string, a: [number, number] | undefined, b: [number, number] | undefined): BenchScenarioRow {
  return {
    scenarioId,
    expectedVerdict: "true_positive",
    a: a && { correct: a[0], runs: a[1] },
    b: b && { correct: b[0], runs: b[1] },
  };
}

describe("scenarioChange", () => {
  it.each([
    ["every repeat right, then none", row("s", [2, 2], [0, 2]), "right-to-wrong"],
    ["none right, then every repeat", row("s", [0, 2], [1, 1]), "wrong-to-right"],
    ["every repeat right, then some", row("s", [2, 2], [1, 2]), "other"],
    ["some right, then none", row("s", [1, 2], [0, 2]), "other"],
    ["some right, then another share", row("s", [1, 2], [1, 3]), "other"],
    ["the same share of some right with other counts", row("s", [1, 2], [2, 4]), "same"],
    ["every repeat right in both", row("s", [2, 2], [1, 1]), "same"],
    ["none right in both", row("s", [0, 2], [0, 3]), "same"],
    ["only in run A", row("s", [1, 1], undefined), "missing"],
    ["only in run B", row("s", undefined, [1, 1]), "missing"],
  ])("tells %s", (_name, scenario, want) => {
    expect(scenarioChange(scenario)).toBe(want);
  });
});

describe("compareScenarios", () => {
  it("lists right to wrong first, then wrong to right, then the rest, each by scenario ID", () => {
    const rows = [
      row("a-same", [1, 1], [1, 1]),
      row("z-worse", [1, 1], [0, 1]),
      row("b-better", [0, 1], [1, 1]),
      row("a-worse", [2, 2], [0, 2]),
      row("a-better", [0, 2], [2, 2]),
      row("c-only-b", undefined, [1, 1]),
      row("b-partly", [1, 2], [0, 2]),
    ];

    expect(compareScenarios(rows).map((r) => r.scenarioId)).toEqual(["a-worse", "z-worse", "a-better", "b-better", "a-same", "b-partly", "c-only-b"]);
  });

  it("leaves the rows it was given in their order", () => {
    const rows = [row("b", [1, 1], [1, 1]), row("a", [1, 1], [0, 1])];

    compareScenarios(rows);

    expect(rows.map((r) => r.scenarioId)).toEqual(["b", "a"]);
  });
});
