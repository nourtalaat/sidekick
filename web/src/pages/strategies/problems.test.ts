import { describe, expect, it } from "vitest";
import { saveProblems } from "./problems";

describe("saveProblems", () => {
  it("puts the reason apart from the problems of the YAML", () => {
    expect(
      saveProblems([
        { field: "reason", message: "a reason is required" },
        { field: "body.match.textPattern", message: "invalid regular expression (line 4)" },
        { field: "body", message: "line 2: did not find expected node content" },
        { field: "body.watchers[0].check.type", message: "expected one of [http tool]" },
      ]),
    ).toEqual({
      reason: "a reason is required",
      body: ["match.textPattern: invalid regular expression (line 4)", "line 2: did not find expected node content", "watchers[0].check.type: expected one of [http tool]"],
    });
  });

  it("keeps a problem of another field and one with no field", () => {
    expect(
      saveProblems([
        { field: "baseVersion", message: "is negative" },
        { field: "", message: "the body is not valid" },
      ]),
    ).toEqual({ body: ["baseVersion: is negative", "the body is not valid"] });
  });

  it("has nothing for no problems", () => {
    expect(saveProblems([])).toEqual({ body: [] });
  });
});
