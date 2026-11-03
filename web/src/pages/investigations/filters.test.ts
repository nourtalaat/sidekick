import { describe, expect, it } from "vitest";
import { SEARCH_MAX_LENGTH, searchPath } from "./filters";

describe("searchPath", () => {
  it.each([
    { name: "a word", text: "checkout", want: "/investigations?q=checkout" },
    { name: "spaces around the text", text: "  disk full  ", want: "/investigations?q=disk+full" },
    { name: "a phrase and an excluded word", text: '"disk full" -test', want: "/investigations?q=%22disk+full%22+-test" },
    { name: "an empty text", text: "", want: "/investigations" },
    { name: "only spaces", text: "   ", want: "/investigations" },
  ])("gives the list for $name", ({ text, want }) => {
    expect(searchPath(text)).toBe(want);
  });

  it("cuts a long text to the most the list field takes", () => {
    const path = searchPath("a".repeat(SEARCH_MAX_LENGTH + 50));

    expect(path).toBe(`/investigations?q=${"a".repeat(SEARCH_MAX_LENGTH)}`);
  });
});
