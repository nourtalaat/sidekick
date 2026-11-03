import type { ProposalRevision } from "../../api/proposals";
import { formatTime } from "../../format";

/** What a screen shows for a fact that the platform does not say. */
const UNKNOWN = "unknown";

/** One revision as lines of plain text: the revision, its version, when and by whom it was deployed, and notes when there are any. */
export function revisionText(r: ProposalRevision): string {
  const lines = [
    `Revision: ${r.kind} ${r.id}`,
    `Version: ${r.version ?? UNKNOWN}`,
    `Deployed at: ${formatTime(r.deployedAt) || UNKNOWN}`,
    `Deployed by: ${r.deployedBy ?? UNKNOWN}`,
  ];

  if (r.notes) {
    lines.push(`Notes: ${r.notes}`);
  }

  return lines.join("\n");
}

/** A revision in one line: its version, or its kind and ID when it has no version. */
export function revisionName(r: ProposalRevision): string {
  return r.version ?? `${r.kind} ${r.id}`;
}
