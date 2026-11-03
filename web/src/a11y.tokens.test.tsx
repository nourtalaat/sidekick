import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Token } from "./api/tokens";
import { json, renderApp, stubMedia, wcagViolations } from "./test/helpers";
import { ADMIN, recording } from "./test/rules";
import { THEME_STORAGE_KEY } from "./theme";

// The same four ways to get a theme as the other accessibility tests.
const THEMES = [
  { name: "system, light", stored: undefined, dark: false },
  { name: "system, dark", stored: undefined, dark: true },
  { name: "light chosen", stored: "light", dark: true },
  { name: "dark chosen", stored: "dark", dark: false },
] as const;

const SECRET = `skt_${"k3Hq9".repeat(8)}`;

function token(id: string, over: Partial<Token>): Token {
  return { id, name: id, role: "viewer", createdBy: "u-admin-1", createdAt: "2026-10-20T10:00:00Z", expiresAt: "2099-01-01T00:00:00Z", ...over };
}

const TOKENS = [
  token("0192d3a4-0000-7000-8000-0000000000a1", { name: "nightly bench", lastUsedAt: "2026-11-01T08:30:00Z" }),
  token("0192d3a4-0000-7000-8000-0000000000a3", { name: "old import", role: "approver", createdAt: "2026-06-01T10:00:00Z", expiresAt: "2026-09-01T00:00:00Z" }),
  token("0192d3a4-0000-7000-8000-0000000000a4", { name: "leaked key", role: "admin", createdAt: "2026-10-27T10:00:00Z", revokedAt: "2026-10-28T09:00:00Z" }),
];

function renderTokens() {
  const { handler } = recording((request) => {
    if (request.method === "POST") {
      return json(201, { token: TOKENS[0], secret: SECRET });
    }

    return json(200, { tokens: TOKENS });
  });

  return renderApp({ me: ADMIN, route: "/settings?tab=tokens", handler });
}

describe.each(THEMES)("accessibility of the API tokens tab, theme $name", (theme) => {
  describe.each([false, true])("forced colors %s", (forcedColors) => {
    beforeEach(() => {
      stubMedia({ dark: theme.dark, forcedColors });

      if (theme.stored) {
        localStorage.setItem(THEME_STORAGE_KEY, theme.stored);
      }
    });

    afterEach(() => {
      localStorage.clear();
      document.documentElement.removeAttribute("data-theme");
    });

    it("has no WCAG 2.2 AA violations with an active, an expired and a revoked token", async () => {
      const { container } = renderTokens();
      await screen.findByRole("region", { name: "API tokens" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations with no tokens", async () => {
      const { handler } = recording(() => json(200, { tokens: [] }));
      const { container } = renderApp({ me: ADMIN, route: "/settings?tab=tokens", handler });
      await screen.findByRole("heading", { name: "No API tokens yet" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations with the secret alert open", async () => {
      const user = userEvent.setup();
      const { container } = renderTokens();

      await user.type(await screen.findByRole("textbox", { name: "Name" }), "ci upload");
      await user.click(screen.getByRole("button", { name: "Create token" }));
      await screen.findByText(SECRET);

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations with the confirm step open", async () => {
      const user = userEvent.setup();
      const { container } = renderTokens();

      await user.click(await screen.findByRole("button", { name: "Revoke token nightly bench" }));
      expect(screen.getByRole("group", { name: "Revoke this token" })).toBeInTheDocument();

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations with the date field and its errors shown", async () => {
      const user = userEvent.setup();
      const { container } = renderTokens();

      await user.selectOptions(await screen.findByRole("combobox", { name: "Expires" }), "date");
      await user.click(screen.getByRole("button", { name: "Create token" }));
      await screen.findByText("Enter a name.");
      await screen.findByText("Pick the last day the token works.");

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations with both the secret alert and the confirm step open", async () => {
      const user = userEvent.setup();
      const { container } = renderTokens();

      await user.type(await screen.findByRole("textbox", { name: "Name" }), "ci upload");
      await user.click(screen.getByRole("button", { name: "Create token" }));
      await screen.findByText(SECRET);
      await user.click(await screen.findByRole("button", { name: "Revoke token nightly bench" }));

      expect(screen.getByText(SECRET)).toBeInTheDocument();
      expect(screen.getByRole("group", { name: "Revoke this token" })).toBeInTheDocument();
      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});
