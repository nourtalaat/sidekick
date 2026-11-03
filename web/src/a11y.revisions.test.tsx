import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ProposalCard } from "./components/ProposalCard";
import { proposal } from "./test/actionProposals";
import { renderWithProviders, stubMedia, wcagViolations } from "./test/helpers";
import { THEME_STORAGE_KEY } from "./theme";

// The same four ways to get a theme as the other accessibility tests.
const THEMES = [
  { name: "system, light", stored: undefined, dark: false },
  { name: "system, dark", stored: undefined, dark: true },
  { name: "light chosen", stored: "light", dark: true },
  { name: "dark chosen", stored: "dark", dark: false },
] as const;

const ROLLBACK = {
  target: "kubernetes:deployment/shop/checkout",
  changes: [{ path: "id", before: '"7"', after: '"6"' }],
  revisions: {
    running: { kind: "ReplicaSet revision", id: "7", version: "checkout:1.4.7", deployedAt: "2026-11-03T09:30:00Z", deployedBy: "ana", notes: "app=checkout:1.4.7" },
    goesBackTo: { kind: "ReplicaSet revision", id: "6", version: "x".repeat(300) },
  },
};

describe.each(THEMES)("accessibility of the revisions of a rollback, theme $name", (theme) => {
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

    it("has no WCAG 2.2 AA violations on a rollback that waits for a decision", async () => {
      const { container } = renderWithProviders(<ProposalCard proposal={proposal({ action: "checkout-rollback", ...ROLLBACK })} canDecide inQueue />);

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on a rollback that is done and can be undone", async () => {
      const { container } = renderWithProviders(<ProposalCard proposal={proposal({ action: "checkout-rollback", status: "succeeded", undoable: true, ...ROLLBACK })} canDecide />);

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});
