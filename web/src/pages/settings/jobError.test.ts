import { describe, expect, it } from "vitest";
import { ERROR_PREVIEW_CHARS, errorPreview } from "./jobError";

describe("errorPreview", () => {
  it("shows a short text whole", () => {
    expect(errorPreview("model: overloaded")).toEqual({ text: "model: overloaded", cut: false });
  });

  it("keeps the first three lines and marks the cut", () => {
    expect(errorPreview("one\ntwo\nthree\nfour")).toEqual({ text: "one\ntwo\nthree…", cut: true });
  });

  it("keeps exactly three lines whole", () => {
    expect(errorPreview("one\ntwo\nthree")).toEqual({ text: "one\ntwo\nthree", cut: false });
  });

  it("cuts a long line at the character limit", () => {
    const result = errorPreview("x".repeat(ERROR_PREVIEW_CHARS + 50));

    expect(result.cut).toBe(true);
    expect(Array.from(result.text)).toHaveLength(ERROR_PREVIEW_CHARS + 1);
    expect(result.text.endsWith("…")).toBe(true);
  });

  it("does not split a character that takes two code units", () => {
    const result = errorPreview("😀".repeat(ERROR_PREVIEW_CHARS + 1));

    expect(result.text).toBe(`${"😀".repeat(ERROR_PREVIEW_CHARS)}…`);
  });
});
