import { describe, expect, it } from "vitest";
import { hasRole, type Role } from "./roles";

describe("hasRole", () => {
  const roles: Role[] = ["viewer", "responder", "approver", "admin"];

  it.each(roles.flatMap((role, i) => roles.map((needed, j) => ({ role, needed, want: i >= j }))))("$role includes $needed: $want", ({ role, needed, want }) => {
    expect(hasRole(role, needed)).toBe(want);
  });
});
