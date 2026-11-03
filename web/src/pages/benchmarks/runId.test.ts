import { describe, expect, it } from "vitest";
import { benchRunId } from "../../test/bench";
import { isRunId } from "./runId";

describe("isRunId", () => {
  it.each([
    ["a UUID", benchRunId(1), true],
    ["a UUID in capitals", benchRunId(10).toUpperCase(), true],
    ["the word compare", "compare", false],
    ["an empty value", "", false],
    ["a UUID with a suffix", `${benchRunId(1)}x`, false],
    ["a UUID with a prefix", `x${benchRunId(1)}`, false],
  ])("answers for %s", (_name, value, want) => {
    expect(isRunId(value)).toBe(want);
  });
});
