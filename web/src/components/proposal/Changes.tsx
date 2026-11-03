import { deletes } from "./deletes";
import { ChangeList } from "./ChangeList";
import type { SectionProps } from "./section";

/** The warning of a delete, and the difference of a definition field by field. */
export function Changes({ proposal }: SectionProps) {
  const changes = proposal.changes ?? [];

  return (
    <>
      {deletes(proposal) && (
        <p className="sk-proposal__warning" role="note">
          This deletes {proposal.target ?? "the target"}. Sidekick cannot undo a delete.
        </p>
      )}
      {changes.length > 0 && <ChangeList changes={changes} heading="Changes, field by field" />}
    </>
  );
}
