import type { Token } from "../../api/tokens";

export type TokenStatus = "active" | "expired" | "revoked";

/** Whether a token works, ran out or was revoked at now. A revoked token that is also past its expiry counts as revoked. */
export function tokenStatus(token: Token, now: Date): TokenStatus {
  if (token.revokedAt) {
    return "revoked";
  }

  return new Date(token.expiresAt).getTime() > now.getTime() ? "active" : "expired";
}

/**
 * The tokens in the order the list shows them: those that work first, then
 * the expired and revoked ones, each group newest first. Tokens made at the
 * same time stay in the order the API gave them.
 */
export function sortTokens(tokens: readonly Token[], now: Date): Token[] {
  const rank = (token: Token) => (tokenStatus(token, now) === "active" ? 0 : 1);

  return tokens
    .map((token, index) => ({ token, index }))
    .sort((a, b) => rank(a.token) - rank(b.token) || createdTime(b.token) - createdTime(a.token) || a.index - b.index)
    .map(({ token }) => token);
}

function createdTime(token: Token): number {
  return new Date(token.createdAt).getTime();
}
