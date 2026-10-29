import { describe, expect, it } from "vitest";
import { capUsed, costChange, countChange, durationChange, formatCount, formatShare, shareChange } from "./words";

describe("shareChange", () => {
  it.each([
    [0.8, 0.76, "Up 4 points"],
    [0.74, 0.76, "Down 2 points"],
    [0.77, 0.76, "Up 1 point"],
    [0.76, 0.76, "No change"],
    [0.764, 0.76, "No change"],
  ])("says how %s changed from %s: %s", (current, previous, want) => {
    expect(shareChange(current, previous)).toBe(want);
  });

  it("says so when a range has no share", () => {
    expect(shareChange(undefined, 0.5)).toBe("No figure for the range before");
    expect(shareChange(0.5, undefined)).toBe("No figure for the range before");
  });
});

describe("countChange", () => {
  it.each([
    [112, 100, "Up 12 percent"],
    [88, 100, "Down 12 percent"],
    [100, 100, "No change"],
    [5, 0, "None in the range before"],
    [0, 0, "No change"],
  ])("says how %s changed from %s: %s", (current, previous, want) => {
    expect(countChange(current, previous)).toBe(want);
  });
});

describe("durationChange", () => {
  it("writes the difference as a length of time", () => {
    expect(durationChange(50_000, 30_000)).toBe("Up 20 s");
    expect(durationChange(30_000, 90_000)).toBe("Down 1 min");
    expect(durationChange(30_000, 30_000)).toBe("No change");
  });

  it("says so when a range has no figure", () => {
    expect(durationChange(undefined, 1000)).toBe("No figure for the range before");
  });
});

describe("costChange", () => {
  it("writes the difference as an amount", () => {
    expect(costChange(25_000, 5_000)).toBe("Up $0.0200");
    expect(costChange(1_000, 3_000)).toBe("Down $0.0020");
    expect(costChange(1_000, 1_000)).toBe("No change");
  });
});

describe("formatShare and capUsed", () => {
  it("rounds to a whole percentage", () => {
    expect(formatShare(0.764)).toBe("76%");
    expect(formatShare(1)).toBe("100%");
    expect(formatShare(undefined)).toBe("none yet");
    expect(capUsed(82, 100)).toBe("82% of the cap");
  });

  it("separates thousands", () => {
    expect(formatCount(1234567)).toBe("1,234,567");
  });
});
