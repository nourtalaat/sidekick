import type { ProposalChange } from "../../api/proposals";
import { BeforeAfter } from "./BeforeAfter";
import "./ChangeList.css";

/** What a side of a change shows when it has no value. */
const NOT_SET = "(not set)";
const REMOVED = "(removed)";
const WHOLE_DEFINITION = "(the whole definition)";

/** The words that name the two values of a change. */
export interface Sides {
  before: string;
  after: string;
}

export const CHANGED: Sides = { before: "Before", after: "After" };
export const RESTORE: Sides = { before: "Now", after: "Restored to" };

/**
 * The difference of a definition, field by field. The values are text that
 * the server wrote with hidden characters spelled out, from the monitoring
 * tool and the model, so they are shown as plain text.
 */
export function ChangeList({ changes, heading, sides = CHANGED }: { changes: ProposalChange[]; heading: string; sides?: Sides }) {
  return (
    <section aria-label={heading} className="sk-changes">
      <h4 className="sk-proposal__label">{heading}</h4>
      <ul className="sk-changes__list">
        {changes.map((c) => (
          <li key={c.path}>
            <BeforeAfter
              title={c.path === "" ? WHOLE_DEFINITION : c.path}
              beforeLabel={sides.before}
              before={c.before ?? NOT_SET}
              afterLabel={sides.after}
              after={c.after ?? REMOVED}
            />
          </li>
        ))}
      </ul>
    </section>
  );
}
