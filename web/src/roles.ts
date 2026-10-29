import type { components } from "./api/schema";

export type Role = components["schemas"]["Role"];

const ROLE_ORDER: readonly Role[] = ["viewer", "responder", "approver", "admin"];

/** Whether have includes need: a higher role includes every lower one. */
export function hasRole(have: Role, need: Role): boolean {
  return ROLE_ORDER.indexOf(have) >= ROLE_ORDER.indexOf(need);
}
