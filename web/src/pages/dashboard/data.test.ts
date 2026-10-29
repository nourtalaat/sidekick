import { describe, expect, it } from "vitest";
import { cost, operations, quality } from "../../test/stats";
import { accuracyRows, comparisonRows, costPerInvestigationRows, dayLabel, eachDay, falseAlarmRows, firstFindingRows, hourLabel, MODEL_SERIES_LIMIT, spendByModel, toolCallRows, verdictRows } from "./data";
import { hasData } from "./chart";

describe("eachDay", () => {
  it("lists every UTC day from the first to the last", () => {
    expect(eachDay("2026-10-30", "2026-11-02")).toEqual(["2026-10-30", "2026-10-31", "2026-11-01", "2026-11-02"]);
  });

  it("lists one day for a range of one", () => {
    expect(eachDay("2026-10-20", "2026-10-20")).toEqual(["2026-10-20"]);
  });
});

describe("labels", () => {
  it("writes a day and an hour in UTC", () => {
    expect(dayLabel("2026-10-20")).toBe("Oct 20");
    expect(hourLabel("2026-10-20T09:00:00Z")).toBe("Oct 20, 09:00 UTC");
  });
});

describe("accuracyRows", () => {
  it("gives a row for each day, with no accuracy on a day nobody rated", () => {
    const rows = accuracyRows(quality(), eachDay("2026-10-19", "2026-10-21"));

    expect(rows.map((r) => r.x)).toEqual(["Oct 19", "Oct 20", "Oct 21"]);
    expect(rows[0]).toMatchObject({ accuracy: undefined, rated: 0 });
    expect(rows[1]?.accuracy).toBeCloseTo((11 / 13) * 100, 6);
    expect(rows[2]?.accuracy).toBeCloseTo(50, 6);
  });
});

describe("comparisonRows", () => {
  it("names each model and strategy", () => {
    expect(comparisonRows(quality()).map((r) => r.x)).toEqual(["big-model / generic-alert", "small-model / deep"]);
  });

  it("names a missing model and strategy", () => {
    const rows = comparisonRows(quality({ comparison: [{ model: "", strategy: "", rated: 2, correct: 1 }] }));

    expect(rows[0]?.x).toBe("no model / no strategy");
  });
});

describe("spendByModel", () => {
  const days = eachDay("2026-10-20", "2026-10-22");

  it("stacks the models by spend, in palette order, with zero for a day without spend", () => {
    const spend = spendByModel(cost(), days);

    expect(spend.series.map((s) => [s.key, s.color])).toEqual([
      ["big-model", "var(--sk-chart-1)"],
      ["small-model", "var(--sk-chart-2)"],
    ]);
    expect(spend.rows[0]).toMatchObject({ x: "Oct 20", "big-model": 20_000, "small-model": 5_000 });
    expect(spend.rows[2]).toMatchObject({ x: "Oct 22", "big-model": 0, "small-model": 0 });
    expect(spend.tokens.in[0]?.tokens).toBe(12_000);
    expect(spend.tokens.out[0]?.tokens).toBe(6_000);
  });

  it("folds the models past the palette into Other", () => {
    const models = Array.from({ length: 10 }, (_, i) => `model-${i}`);
    const stats = cost({
      dayModels: models.map((model, i) => ({ day: "2026-10-20", connector: "c", model, costMicros: 1000 - i * 10, inputTokens: 1, outputTokens: 1 })),
    });
    const spend = spendByModel(stats, days);

    expect(spend.series).toHaveLength(MODEL_SERIES_LIMIT + 1);
    expect(spend.series.at(-1)).toMatchObject({ label: "Other" });
    // The three smallest add up: 1000-70, 1000-80, 1000-90.
    expect(spend.rows[0]?.[spend.series.at(-1)?.key ?? ""]).toBe(930 + 920 + 910);
  });
});

describe("costPerInvestigationRows", () => {
  it("divides the cost by the investigations of each day", () => {
    const rows = costPerInvestigationRows(cost(), eachDay("2026-10-20", "2026-10-22"));

    expect(rows[0]?.cost).toBeCloseTo(40_000 / 14, 6);
    expect(rows[2]?.cost).toBeUndefined();
  });
});

describe("operations rows", () => {
  const days = eachDay("2026-10-20", "2026-10-22");

  it("adds Duplicate and Known issue into one series and keeps them apart for the table", () => {
    const rows = verdictRows(operations(), days);

    expect(rows[0]).toMatchObject({ false_positive: 10, true_positive: 4, duplicate_or_known: 0 });
    expect(rows[1]).toMatchObject({ duplicate: 2, known_issue: 1, duplicate_or_known: 3, none: 1 });
    expect(rows[2]).toMatchObject({ false_positive: 0, duplicate_or_known: 0 });
  });

  it("means the time to the first finding over the investigations that have one", () => {
    const rows = firstFindingRows(operations(), days);

    expect(rows[0]?.time).toBeCloseTo(520_000 / 12, 6);
    expect(rows[2]?.time).toBeUndefined();
  });

  it("means the tool calls over the investigations", () => {
    expect(toolCallRows(operations(), days)[0]?.calls).toBeCloseTo(90 / 14, 6);
  });

  it("counts the false alarms of each day", () => {
    expect(falseAlarmRows(operations(), days).map((r) => r.count)).toEqual([10, 4, 0]);
  });

  it("uses the hours of the series for an interval of an hour", () => {
    const stats = operations({ interval: "hour", series: operations().series.slice(0, 1) });

    expect(verdictRows(stats, days).map((r) => r.x)).toEqual(["Oct 20, 00:00 UTC"]);
  });
});

describe("hasData", () => {
  const spec = { kind: "bar" as const, series: [{ key: "n", label: "N", color: "red" }], xLabel: "x", format: String };

  it("needs a bar above zero", () => {
    expect(hasData({ ...spec, data: [{ x: "a", n: 0 }] })).toBe(false);
    expect(hasData({ ...spec, data: [{ x: "a", n: 0 }, { x: "b", n: 2 }] })).toBe(true);
  });

  it("takes a line at zero as data, but not a gap", () => {
    expect(hasData({ ...spec, kind: "line", data: [{ x: "a", n: 0 }] })).toBe(true);
    expect(hasData({ ...spec, kind: "line", data: [{ x: "a", n: undefined }] })).toBe(false);
  });
});
