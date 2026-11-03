import type { ProposalStatus } from "../../api/proposals";

/** One choice of the Status menu: the word in the address, the label, the statuses it asks for and the title of an empty list. */
export interface StatusFilter {
  word: string;
  label: string;
  statuses: readonly ProposalStatus[];
  /** What an empty list says, before " for <action>" is added when an action is chosen. */
  empty: string;
}

/** The choice when the address names none: the proposals that wait for a decision. */
export const WAITING_FILTER: StatusFilter = { word: "waiting", label: "Waiting for a decision", statuses: ["proposed"], empty: "Nothing waits for a decision" };

/** The choices of the Status menu, in the order they are shown. */
export const STATUS_FILTERS: readonly StatusFilter[] = [
  WAITING_FILTER,
  { word: "approved", label: "Approved", statuses: ["approved", "executing"], empty: "No approved actions" },
  { word: "ran", label: "Ran", statuses: ["succeeded"], empty: "No actions ran" },
  { word: "failed", label: "Failed", statuses: ["failed"], empty: "No failed actions" },
  { word: "expired", label: "Expired", statuses: ["expired"], empty: "No expired actions" },
  { word: "undone", label: "Undone", statuses: ["undone"], empty: "No undone actions" },
  { word: "rejected", label: "Rejected", statuses: ["rejected"], empty: "No rejected actions" },
  {
    word: "any",
    label: "Any status",
    statuses: ["proposed", "approved", "executing", "succeeded", "failed", "expired", "undone", "rejected"],
    empty: "No action proposals",
  },
];

/** The choice for a word of the address; a word that is none of them gives the default. */
export function statusFilterOf(word: string | null): StatusFilter {
  return STATUS_FILTERS.find((filter) => filter.word === word) ?? WAITING_FILTER;
}

/** The title of an empty list, which names the filters in use. */
export function emptyTitle(filter: StatusFilter, action: string): string {
  if (action !== "") {
    return `${filter.empty} for ${action}`;
  }

  return filter.word === "any" ? `${filter.empty} yet` : filter.empty;
}

/** The line under the title of an empty list: what would fill it, or how to see other proposals. */
export function emptyText(filter: StatusFilter, action: string): string {
  if (action === "" && filter.word === WAITING_FILTER.word) {
    return "When the model proposes an action on a route in approved mode, it shows here.";
  }

  if (action === "" && filter.word === "any") {
    return "Actions the model proposes show here.";
  }

  return "Change the action or the status to see other proposals.";
}
