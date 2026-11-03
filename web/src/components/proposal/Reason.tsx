import type { SectionProps } from "./section";

/** The model's reason, marked as unchecked. An undo has none. */
export function Reason({ proposal }: SectionProps) {
  if (proposal.undoOf) {
    return null;
  }

  return (
    <>
      <p className="sk-proposal__label">Reason, written by the model and not checked</p>
      <p className="sk-proposal__text">{proposal.reason}</p>
    </>
  );
}
