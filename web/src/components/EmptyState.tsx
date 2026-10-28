import type { ReactNode } from "react";
import emptyEyes from "../../../assets/states/empty.svg";
import "./EmptyState.css";

/**
 * The state of a list or page with nothing in it: the character looking up,
 * a short title and a line that says what to expect.
 */
export function EmptyState({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="sk-empty">
      <img src={emptyEyes} alt="" width={120} height={120} />
      <h2 className="sk-empty__title">{title}</h2>
      <p className="sk-empty__text">{children}</p>
    </div>
  );
}
