import { screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { THEME_STORAGE_KEY } from "./theme";
import { ProposalCard } from "./components/ProposalCard";
import { APPROVER, PROPOSAL_ID, fileDiff, proposal, proposalFile } from "./test/actionProposals";
import { fakeServer, json, problem, renderWithProviders, stubMedia, wcagViolations } from "./test/helpers";
import { routes } from "./test/streams";

// The same four ways to get a theme as the other accessibility tests.
const THEMES = [
  { name: "system, light", stored: undefined, dark: false },
  { name: "system, dark", stored: undefined, dark: true },
  { name: "light chosen", stored: "light", dark: true },
  { name: "dark chosen", stored: "dark", dark: false },
] as const;

const PATH = "prometheus/rules/checkout.yaml";

/** A card for a proposal with two files, whose diff is answered with answer. */
function renderCard(answer: () => Response) {
  const { api } = fakeServer(APPROVER, routes({ [`GET /api/v1/proposals/${PROPOSAL_ID}/files/diff`]: answer }));
  const files = [proposalFile({ path: PATH }), proposalFile({ path: "prometheus/rules/new.yaml", status: "added", linesAdded: 2, linesRemoved: 0 })];

  return renderWithProviders(<ProposalCard proposal={proposal({ files })} canDecide />, { api });
}

describe.each(THEMES)("accessibility of the files of a pull request proposal, theme $name", (theme) => {
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

    it("has no WCAG 2.2 AA violations on a card with a diff open", async () => {
      const { container } = renderCard(() => json(200, fileDiff({ truncated: true, finalNewline: "added" })));
      await waitFor(() => expect(screen.getAllByRole("region", { name: /^Changes in / })).toHaveLength(2));

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on a file that could not be read", async () => {
      const { container } = renderCard(() => problem(502, "Sidekick could not read the file on the base branch from the host: not found"));
      await waitFor(() => expect(screen.getAllByRole("alert")).toHaveLength(2));

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});
