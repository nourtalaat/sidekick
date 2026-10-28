import { describe, expect, it } from "vitest";
import { formatCost, formatDuration, formatTokens } from "./format";

describe("formatDuration", () => {
  it.each([
    { ms: undefined, want: "" },
    { ms: 850, want: "850 ms" },
    { ms: 12_000, want: "12 s" },
    { ms: 240_000, want: "4 min" },
    { ms: 245_000, want: "4 min 5 s" },
    { ms: 3_780_000, want: "1 h 3 min" },
    { ms: 7_200_000, want: "2 h" },
  ])("$ms gives $want", ({ ms, want }) => {
    expect(formatDuration(ms)).toBe(want);
  });
});

describe("formatCost", () => {
  it.each([
    { micros: undefined, want: "" },
    { micros: 0, want: "$0.0000" },
    { micros: 12_300, want: "$0.0123" },
    { micros: 1_200_000, want: "$1.20" },
  ])("$micros gives $want", ({ micros, want }) => {
    expect(formatCost(micros)).toBe(want);
  });
});

describe("formatTokens", () => {
  it.each([
    { count: undefined, want: "" },
    { count: 950, want: "950" },
    { count: 12_345, want: "12.3k" },
  ])("$count gives $want", ({ count, want }) => {
    expect(formatTokens(count)).toBe(want);
  });
});
