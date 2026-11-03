import type { Proposal } from "../../api/proposals";

/** Whether the proposal removes what it changes: a change of the whole definition with no new value. */
export function deletes(p: Proposal): boolean {
  return (p.changes ?? []).some((c) => c.path === "" && c.after === undefined);
}
