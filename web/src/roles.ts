import type { Me } from "./session";

export type Role = Me["role"];

const RANK: Record<Role, number> = { viewer: 1, responder: 2, approver: 3, admin: 4 };

/**
 * Whether role includes needed: a higher role includes every lower one. The
 * server checks the role of every request again; this only decides which
 * buttons the page shows.
 */
export function hasRole(role: Role, needed: Role): boolean {
  return RANK[role] >= RANK[needed];
}
