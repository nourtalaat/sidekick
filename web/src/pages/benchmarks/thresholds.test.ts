import { describe, expect, it } from "vitest";
import type { BenchThreshold } from "../../api/bench";
import { failedThresholds, thresholdLimitText, thresholdName, thresholdValueText } from "./thresholds";

const share = (over: Partial<BenchThreshold> = {}): BenchThreshold => ({ name: "verdictAccuracy", limit: 0.8, value: 0.725, passed: false, ...over });
const cost = (over: Partial<BenchThreshold> = {}): BenchThreshold => ({ name: "maxCostPerRunUSD", limit: 0.001, value: 0.002, passed: false, ...over });

describe("thresholdName", () => {
  it.each([
    { name: "verdictAccuracy", want: "Verdict accuracy" },
    { name: "fpPrecision", want: "False positive precision" },
    { name: "rootCauseMatch", want: "Root cause match" },
    { name: "proposalCorrect", want: "Proposals correct" },
    { name: "maxCostPerRunUSD", want: "Mean cost per run" },
    { name: "baseline", want: "Overall score against the baseline" },
    { name: "somethingNew", want: "somethingNew" },
  ])("names $name as $want", ({ name, want }) => {
    expect(thresholdName(name)).toBe(want);
  });
});

describe("threshold text", () => {
  it.each([
    { name: "a share", t: share(), limit: "at least 80%", value: "72.5%" },
    { name: "a share that is a whole percent", t: share({ value: 0.8 }), limit: "at least 80%", value: "80%" },
    { name: "a share just under its limit", t: share({ value: 0.7996 }), limit: "at least 80%", value: "80%" },
    { name: "a share with no value", t: share({ value: undefined }), limit: "at least 80%", value: "none yet" },
    { name: "the cost", t: cost(), limit: "at most $0.0010", value: "$0.0020" },
  ])("shows $name", ({ t, limit, value }) => {
    expect(thresholdLimitText(t)).toBe(limit);
    expect(thresholdValueText(t)).toBe(value);
  });
});

describe("failedThresholds", () => {
  it("keeps the failed ones in the order given", () => {
    const list = [share({ passed: true }), share({ name: "rootCauseMatch" }), cost()];

    expect(failedThresholds(list).map((t) => t.name)).toEqual(["rootCauseMatch", "maxCostPerRunUSD"]);
    expect(failedThresholds([])).toEqual([]);
  });
});
