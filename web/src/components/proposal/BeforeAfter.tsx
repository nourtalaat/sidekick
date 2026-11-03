import type { ReactElement } from "react";
import "./BeforeAfter.css";

export interface BeforeAfterProps {
  /** Names the pair, for example a field path. Plain text. */
  title: string;
  beforeLabel: string;
  before: string;
  afterLabel: string;
  after: string;
}

/**
 * Two values of one thing, each under its label, in a block that scrolls and
 * takes focus: side by side on a wide screen, one above the other on a
 * narrow one. The texts are shown as plain text. The component does not know
 * what the pair is, so the caller chooses the labels, such as "Before" and
 * "After", and the words for a side that has no value.
 */
export function BeforeAfter({ title, beforeLabel, before, afterLabel, after }: BeforeAfterProps): ReactElement {
  return (
    <div className="sk-before-after">
      <h5 className="sk-before-after__title">{title}</h5>
      <div className="sk-before-after__sides">
        <Side title={title} label={beforeLabel} text={before} kind="before" />
        <Side title={title} label={afterLabel} text={after} kind="after" />
      </div>
    </div>
  );
}

function Side({ title, label, text, kind }: { title: string; label: string; text: string; kind: "before" | "after" }) {
  return (
    <div className="sk-before-after__side">
      <p className="sk-before-after__label">{label}</p>
      <pre className={`sk-before-after__text sk-before-after__text--${kind}`} role="region" aria-label={`${label}, ${title}`} tabIndex={0}>
        {text}
      </pre>
    </div>
  );
}
