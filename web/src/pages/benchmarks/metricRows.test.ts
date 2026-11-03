import { describe, expect, it } from "vitest";
import { benchRunDetail } from "../../test/bench";
import { intervalText, METRIC_ROWS, metricScoreRows } from "./metricRows";

describe("METRIC_ROWS", () => {
  it("names every share metric, in the order the page shows them", () => {
    expect(METRIC_ROWS.map((row) => row.label)).toEqual([
      "Overall score",
      "Verdict accuracy",
      "False positive precision",
      "False positive recall",
      "True positive precision",
      "True positive recall",
      "Root cause match",
      "Root cause score",
      "Evidence",
      "Proposals correct",
      "Source failures reported",
    ]);
  });
});

describe("metricScoreRows", () => {
  it("gives each metric its value and its interval, and none for what the run lacks", () => {
    const run = benchRunDetail(2);
    const rows = metricScoreRows(run.metrics, run.intervals);

    expect(rows).toHaveLength(METRIC_ROWS.length);
    expect(rows[0]).toEqual({ key: "overall", label: "Overall score", value: 0.7, interval: { lower: 0.62, upper: 0.88 } });
    expect(rows.find((r) => r.key === "tpRecall")).toMatchObject({ value: 0.5, interval: undefined });
    expect(rows.find((r) => r.key === "proposalCorrect")).toMatchObject({ value: undefined, interval: undefined });
  });
});

describe("intervalText", () => {
  it.each([
    { name: "an interval", interval: { lower: 0.62, upper: 0.88 }, want: "62% to 88%" },
    { name: "a point", interval: { lower: 1, upper: 1 }, want: "100% to 100%" },
    { name: "no interval", interval: undefined, want: "none" },
  ])("shows $name as $want", ({ interval, want }) => {
    expect(intervalText(interval)).toBe(want);
  });
});
