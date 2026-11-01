import { describe, expect, it } from "vitest";
import { countChanges, diffLines, linesOf, type DiffLine } from "./lineDiff";

/** Each line as "+ text", "- text" or "  text", to compare diffs at a glance. */
function show(lines: DiffLine[]): string[] {
  return lines.map((l) => `${l.kind === "added" ? "+" : l.kind === "removed" ? "-" : " "} ${l.text}`);
}

describe("linesOf", () => {
  it.each([
    { name: "an empty text has no lines", text: "", want: [] },
    { name: "a final newline ends the last line", text: "a\nb\n", want: ["a", "b"] },
    { name: "a text with no final newline", text: "a\nb", want: ["a", "b"] },
    { name: "an empty line in the middle is a line", text: "a\n\nb\n", want: ["a", "", "b"] },
    { name: "one newline is one empty line", text: "\n", want: [""] },
  ])("$name", ({ text, want }) => {
    expect(linesOf(text)).toEqual(want);
  });
});

describe("diffLines", () => {
  it.each([
    { name: "equal texts", before: "a\nb\n", after: "a\nb\n", want: ["  a", "  b"] },
    { name: "a line added at the end", before: "a\n", after: "a\nb\n", want: ["  a", "+ b"] },
    { name: "a line added at the start", before: "b\n", after: "a\nb\n", want: ["+ a", "  b"] },
    { name: "a line removed from the middle", before: "a\nb\nc\n", after: "a\nc\n", want: ["  a", "- b", "  c"] },
    { name: "a line changed", before: "a\nb\nc\n", after: "a\nB\nc\n", want: ["  a", "- b", "+ B", "  c"] },
    { name: "from nothing", before: "", after: "a\nb\n", want: ["+ a", "+ b"] },
    { name: "to nothing", before: "a\nb\n", after: "", want: ["- a", "- b"] },
    { name: "two changes apart", before: "a\nb\nc\nd\ne\n", after: "a\nB\nc\nD\ne\n", want: ["  a", "- b", "+ B", "  c", "- d", "+ D", "  e"] },
    { name: "a block moved down shows as removed and added", before: "a\nb\nc\nd\n", after: "c\nd\na\nb\n", want: ["- a", "- b", "  c", "  d", "+ a", "+ b"] },
    { name: "repeated lines", before: "x\nx\nx\n", after: "x\nx\n", want: ["  x", "  x", "- x"] },
    { name: "a final newline alone is no change", before: "a\nb", after: "a\nb\n", want: ["  a", "  b"] },
  ])("$name", ({ before, after, want }) => {
    expect(show(diffLines(before, after))).toEqual(want);
  });

  it("numbers the lines of each text", () => {
    const lines = diffLines("a\nb\nc\n", "a\nB\nc\nd\n");

    expect(lines).toEqual([
      { kind: "same", text: "a", oldLine: 1, newLine: 1 },
      { kind: "removed", text: "b", oldLine: 2 },
      { kind: "added", text: "B", newLine: 2 },
      { kind: "same", text: "c", oldLine: 3, newLine: 3 },
      { kind: "added", text: "d", newLine: 4 },
    ]);
  });

  it("rebuilds both texts from the diff", () => {
    const before = "name: x\ndescription: Old.\nfirst:\n  - one\n  - two\n";
    const after = "name: x\ndescription: New.\nfirst:\n  - one\n  - three\n  - two\n";
    const lines = diffLines(before, after);

    expect(lines.filter((l) => l.kind !== "added").map((l) => l.text)).toEqual(linesOf(before));
    expect(lines.filter((l) => l.kind !== "removed").map((l) => l.text)).toEqual(linesOf(after));
  });

  it("handles a long strategy", () => {
    const before = Array.from({ length: 1500 }, (_, i) => `line ${i}`).join("\n");
    const after = before.replace("line 700\n", "").replace("line 1200", "changed");

    expect(countChanges(diffLines(before, after))).toEqual({ added: 1, removed: 2 });
  });
});

describe("countChanges", () => {
  it("counts the added and removed lines", () => {
    expect(countChanges(diffLines("a\nb\nc\n", "a\nB\nc\nd\n"))).toEqual({ added: 2, removed: 1 });
    expect(countChanges(diffLines("a\n", "a\n"))).toEqual({ added: 0, removed: 0 });
  });
});
