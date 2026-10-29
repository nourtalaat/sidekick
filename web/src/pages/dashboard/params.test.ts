import { describe, expect, it } from "vitest";
import { filtersFromParams, paramsFromFilters, rangeOf, statsQueryOf, utcDate } from "./params";

const NOW = new Date("2026-10-28T12:00:00Z");
const RUN_ID = "0192d3a4-0000-7000-8000-0000000000a1";

describe("filtersFromParams", () => {
  it("defaults to the last 30 days with no filters", () => {
    expect(filtersFromParams(new URLSearchParams())).toEqual({ range: "30d" });
  });

  it("reads a preset, the filters and the runs to compare", () => {
    const params = new URLSearchParams(`range=90d&route=payments&service=checkout-api&model=big&verdict=false_positive&a=${RUN_ID}&b=${RUN_ID}`);

    expect(filtersFromParams(params)).toEqual({
      range: "90d",
      route: "payments",
      service: "checkout-api",
      model: "big",
      verdict: "false_positive",
      compareA: RUN_ID,
      compareB: RUN_ID,
    });
  });

  it("reads custom dates", () => {
    expect(filtersFromParams(new URLSearchParams("range=custom&from=2026-10-01&to=2026-10-07"))).toEqual({ range: "custom", from: "2026-10-01", to: "2026-10-07" });
  });

  it.each([
    ["a range that is not one", "range=forever"],
    ["custom dates that are not dates", "range=custom&from=yesterday&to=today"],
    ["custom dates out of order", "range=custom&from=2026-10-07&to=2026-10-01"],
    ["custom dates more than 400 days apart", "range=custom&from=2025-01-01&to=2026-10-01"],
    ["custom dates without a range", "from=2026-10-01&to=2026-10-07"],
    ["a custom range without dates", "range=custom"],
  ])("falls back to the default for %s", (_name, query) => {
    expect(filtersFromParams(new URLSearchParams(query))).toEqual({ range: "30d" });
  });

  it("leaves out a verdict, text and run ID the API would refuse", () => {
    const params = new URLSearchParams("verdict=maybe&route=%20%20&a=not-an-id");

    expect(filtersFromParams(params)).toEqual({ range: "30d" });
  });
});

describe("paramsFromFilters", () => {
  it("leaves out the default range and empty filters", () => {
    expect(paramsFromFilters({ range: "30d" }).toString()).toBe("");
  });

  it("round-trips every filter", () => {
    const filters = { range: "custom", from: "2026-10-01", to: "2026-10-07", route: "payments", verdict: "flapping", compareA: RUN_ID, compareB: RUN_ID } as const;

    expect(filtersFromParams(paramsFromFilters(filters))).toEqual(filters);
  });

  it("writes a preset", () => {
    expect(paramsFromFilters({ range: "7d" }).toString()).toBe("range=7d");
  });
});

describe("rangeOf", () => {
  it.each([
    ["7d", "2026-10-22"],
    ["30d", "2026-09-29"],
    ["90d", "2026-07-31"],
    ["365d", "2025-10-29"],
  ])("ends a %s preset today in UTC", (range, from) => {
    expect(rangeOf({ range }, NOW)).toEqual({ from, to: "2026-10-28" });
  });

  it("uses the dates of a custom range", () => {
    expect(rangeOf({ range: "custom", from: "2026-01-01", to: "2026-01-31" }, NOW)).toEqual({ from: "2026-01-01", to: "2026-01-31" });
  });

  it("counts the day in UTC, not in the viewer's zone", () => {
    expect(utcDate(new Date("2026-10-28T23:30:00-05:00"))).toBe("2026-10-29");
  });
});

describe("statsQueryOf", () => {
  it("is the range with the filters that are set", () => {
    expect(statsQueryOf({ range: "7d", route: "payments", verdict: "true_positive" }, NOW)).toEqual({
      from: "2026-10-22",
      to: "2026-10-28",
      route: "payments",
      verdict: "true_positive",
    });
  });
});
