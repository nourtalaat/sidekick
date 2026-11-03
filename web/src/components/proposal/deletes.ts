import type { Proposal } from "../../api/proposals";

/**
 * Whether the proposal removes what it changes: a change of the whole definition with no new value. An undo of
 * something an action created has the same shape, and it is not a delete: it ends what the action made.
 */
export function deletes(p: Proposal): boolean {
  return !p.undoOf && (p.changes ?? []).some((c) => c.path === "" && c.after === undefined);
}

/** Whether the proposal made a definition that did not exist: a change of the whole definition with no value before. */
export function creates(p: Proposal): boolean {
  return (p.changes ?? []).some((c) => c.path === "" && c.before === undefined && c.after !== undefined);
}
