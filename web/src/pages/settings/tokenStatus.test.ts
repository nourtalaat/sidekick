import { describe, expect, it } from "vitest";
import type { Token } from "../../api/tokens";
import { sortTokens, tokenStatus } from "./tokenStatus";

const NOW = new Date("2026-11-03T12:00:00Z");

function token(id: string, over: Partial<Token> = {}): Token {
  return {
    id,
    name: id,
    role: "viewer",
    createdBy: "u1",
    createdAt: "2026-10-01T10:00:00Z",
    expiresAt: "2027-01-01T00:00:00Z",
    ...over,
  };
}

describe("tokenStatus", () => {
  it.each([
    { name: "works before its expiry", over: {}, want: "active" },
    { name: "has run out after its expiry", over: { expiresAt: "2026-11-01T00:00:00Z" }, want: "expired" },
    { name: "has run out at the moment of its expiry", over: { expiresAt: NOW.toISOString() }, want: "expired" },
    { name: "is revoked", over: { revokedAt: "2026-10-20T10:00:00Z" }, want: "revoked" },
    { name: "is revoked even when it is also past its expiry", over: { revokedAt: "2026-10-20T10:00:00Z", expiresAt: "2026-10-25T00:00:00Z" }, want: "revoked" },
  ])("says a token that $name is $want", ({ over, want }) => {
    expect(tokenStatus(token("t", over), NOW)).toBe(want);
  });
});

describe("sortTokens", () => {
  it("puts working tokens first, then the others, each group newest first", () => {
    const tokens = [
      token("old-active", { createdAt: "2026-09-01T00:00:00Z" }),
      token("revoked-newest", { createdAt: "2026-10-30T00:00:00Z", revokedAt: "2026-10-31T00:00:00Z" }),
      token("expired", { createdAt: "2026-08-01T00:00:00Z", expiresAt: "2026-10-01T00:00:00Z" }),
      token("new-active", { createdAt: "2026-10-15T00:00:00Z" }),
    ];

    expect(sortTokens(tokens, NOW).map((t) => t.id)).toEqual(["new-active", "old-active", "revoked-newest", "expired"]);
  });

  it("keeps the order of the API for tokens made at the same time, and does not change its input", () => {
    const tokens = [token("a"), token("b"), token("c")];

    expect(sortTokens(tokens, NOW).map((t) => t.id)).toEqual(["a", "b", "c"]);
    expect(tokens.map((t) => t.id)).toEqual(["a", "b", "c"]);
  });

  it("returns an empty list for an empty list", () => {
    expect(sortTokens([], NOW)).toEqual([]);
  });
});
