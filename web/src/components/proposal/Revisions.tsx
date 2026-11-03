import { BeforeAfter } from "./BeforeAfter";
import { revisionName, revisionText } from "./revisionText";
import type { SectionProps } from "./section";
import "./Revisions.css";

/**
 * The two revisions of a rollback: what is running now and what it goes back
 * to, in place of the field-by-field list. The texts come from the platform,
 * such as an image tag or who deployed, with hidden characters spelled out by
 * the server, so they are shown as plain text.
 */
export function Revisions({ proposal }: SectionProps) {
  const revisions = proposal.revisions;

  if (revisions === undefined) {
    return null;
  }

  const { running, goesBackTo } = revisions;

  return (
    <section aria-label="Revisions" className="sk-revisions">
      <h4 className="sk-proposal__label">Revisions</h4>
      <p className="sk-proposal__text">
        Rolls back from {revisionName(running)} to {revisionName(goesBackTo)}.
      </p>
      <BeforeAfter
        title={running.kind}
        beforeLabel="Running now"
        before={revisionText(running)}
        afterLabel="Goes back to"
        after={revisionText(goesBackTo)}
      />
    </section>
  );
}
