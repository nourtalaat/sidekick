/** How a line of a diff relates to the two texts. */
export type DiffKind = "same" | "added" | "removed";

/** One line of a diff, with its number in the old text, the new text, or both. */
export interface DiffLine {
  kind: DiffKind;
  text: string;
  /** The line number in the old text, from 1; absent for an added line. */
  oldLine?: number;
  /** The line number in the new text, from 1; absent for a removed line. */
  newLine?: number;
}

/** How many lines a diff adds and removes. */
export interface DiffCounts {
  added: number;
  removed: number;
}

/** The lines of text. A final newline ends the last line; it does not start another. */
export function linesOf(text: string): string[] {
  if (text === "") {
    return [];
  }

  const lines = text.split("\n");
  if (lines[lines.length - 1] === "") {
    lines.pop();
  }

  return lines;
}

/**
 * The lines that turn before into after, in order: lines in both texts, lines
 * only in after and lines only in before. It keeps the longest run of lines
 * the two texts share, so a moved block shows as removed and added.
 */
export function diffLines(before: string, after: string): DiffLine[] {
  const a = linesOf(before);
  const b = linesOf(after);

  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) {
    start++;
  }

  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }

  const out: DiffLine[] = [];

  for (let i = 0; i < start; i++) {
    out.push({ kind: "same", text: a[i] ?? "", oldLine: i + 1, newLine: i + 1 });
  }

  out.push(...middle(a, b, start, endA, endB));

  for (let k = 0; k < a.length - endA; k++) {
    out.push({ kind: "same", text: a[endA + k] ?? "", oldLine: endA + k + 1, newLine: endB + k + 1 });
  }

  return out;
}

/** How many lines the diff adds and removes. */
export function countChanges(lines: readonly DiffLine[]): DiffCounts {
  let added = 0;
  let removed = 0;

  for (const line of lines) {
    if (line.kind === "added") {
      added++;
    } else if (line.kind === "removed") {
      removed++;
    }
  }

  return { added, removed };
}

/** The diff of the lines a[start..endA) and b[start..endB), which share no first or last line. */
function middle(a: string[], b: string[], start: number, endA: number, endB: number): DiffLine[] {
  const rows = endA - start;
  const cols = endB - start;

  // lcs[i][j] is the length of the longest run shared by the lines of a from start + i and of b from start + j.
  const width = cols + 1;
  const lcs = new Uint32Array((rows + 1) * width);

  for (let i = rows - 1; i >= 0; i--) {
    for (let j = cols - 1; j >= 0; j--) {
      lcs[i * width + j] =
        a[start + i] === b[start + j] ? (lcs[(i + 1) * width + j + 1] ?? 0) + 1 : Math.max(lcs[(i + 1) * width + j] ?? 0, lcs[i * width + j + 1] ?? 0);
    }
  }

  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;

  while (i < rows || j < cols) {
    const oldLine = start + i + 1;
    const newLine = start + j + 1;

    if (i < rows && j < cols && a[start + i] === b[start + j]) {
      out.push({ kind: "same", text: a[start + i] ?? "", oldLine, newLine });
      i++;
      j++;
    } else if (j >= cols || (i < rows && (lcs[(i + 1) * width + j] ?? 0) >= (lcs[i * width + j + 1] ?? 0))) {
      out.push({ kind: "removed", text: a[start + i] ?? "", oldLine });
      i++;
    } else {
      out.push({ kind: "added", text: b[start + j] ?? "", newLine });
      j++;
    }
  }

  return out;
}
