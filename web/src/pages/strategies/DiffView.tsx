import { useMemo } from "react";
import { countChanges, diffLines } from "../../diff/lineDiff";
import "./Diff.css";

const MARKS = { added: "+", removed: "-", same: "" } as const;
const SPOKEN = { added: "Added: ", removed: "Removed: ", same: "" } as const;

/**
 * The lines that turn one text into another. An added or removed line has a
 * sign and a spoken label as well as its color, so color is never the only
 * signal. label names the region for a screen reader.
 */
export function DiffView({ before, after, label }: { before: string; after: string; label: string }) {
  const lines = useMemo(() => diffLines(before, after), [before, after]);
  const { added, removed } = countChanges(lines);

  if (added === 0 && removed === 0) {
    return <p>The two texts are the same.</p>;
  }

  return (
    <>
      <p className="sk-diff__summary">
        {added} {added === 1 ? "line" : "lines"} added, {removed} {removed === 1 ? "line" : "lines"} removed.
      </p>
      <div className="sk-diff" role="region" aria-label={label} tabIndex={0}>
        <table className="sk-diff__table">
          <thead className="sk-visually-hidden">
            <tr>
              <th scope="col">Line in the old text</th>
              <th scope="col">Line in the new text</th>
              <th scope="col">Text</th>
            </tr>
          </thead>
          <tbody>
            {lines.map((line, i) => (
              <tr key={i} className={`sk-diff__line sk-diff__line--${line.kind}`}>
                <td className="sk-diff__number">{line.oldLine}</td>
                <td className="sk-diff__number">{line.newLine}</td>
                <td className="sk-diff__text">
                  <span aria-hidden="true" className="sk-diff__mark">
                    {MARKS[line.kind]}
                  </span>
                  <span className="sk-visually-hidden">{SPOKEN[line.kind]}</span>
                  {line.text}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
