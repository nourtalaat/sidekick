import { describe, expect, it } from "vitest";
import { benchResult, benchRunDetail } from "../../test/bench";
import { groupScenarios } from "./scenarios";

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
