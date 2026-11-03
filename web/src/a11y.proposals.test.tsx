import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { THEME_STORAGE_KEY } from "./theme";
import { ProposalCard } from "./components/ProposalCard";
import { APPROVER, PROPOSAL_ID, proposal } from "./test/actionProposals";
import { fakeServer, json, renderApp, renderWithProviders, stubMedia, wcagViolations } from "./test/helpers";
import { failedProposal, passedProposal, proposalApi } from "./test/proposals";
import { ADMIN } from "./test/rules";
import { routes } from "./test/streams";

// The same four ways to get a theme as the other accessibility tests.
const THEMES = [
  { name: "system, light", stored: undefined, dark: false },
  { name: "system, dark", stored: undefined, dark: true },
  { name: "light chosen", stored: "light", dark: true },
  { name: "dark chosen", stored: "dark", dark: false },
] as const;

describe.each(THEMES)("accessibility of the strategies tab of the review page, theme $name", (theme) => {
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

    it("has no WCAG 2.2 AA violations on the list", async () => {
      const { handler } = proposalApi({ proposals: [passedProposal(), failedProposal("not_enough_evidence")] });
      const { container } = renderApp({ me: ADMIN, route: "/review?tab=strategies", handler });
      await screen.findAllByRole("article");

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the review of a passed proposal being edited", async () => {
      const user = userEvent.setup();
      const { handler } = proposalApi({ proposals: [passedProposal()] });
      const { container } = renderApp({ me: ADMIN, route: "/review?tab=strategies", handler });

      const card = (await screen.findAllByRole("article"))[0] as HTMLElement;
      await user.click(within(card).getByRole("button", { name: /Review/ }));
      await within(card).findByRole("region", { name: "Benchmark gate" });
      await user.click(within(card).getByRole("button", { name: "Edit" }));

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the review of a proposal that did not pass", async () => {
      const user = userEvent.setup();
      const { handler } = proposalApi({ proposals: [failedProposal()] });
      const { container } = renderApp({ me: ADMIN, route: "/review?tab=strategies", handler });

      const card = (await screen.findAllByRole("article"))[0] as HTMLElement;
      await user.click(within(card).getByRole("button", { name: /Review/ }));
      expect(await within(card).findByText("The change scores lower than the current version")).toBeInTheDocument();

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});

const CHANGES = [
  { path: "options.thresholds.critical", before: "2", after: "3" },
  { path: "tags", before: '["a"]' },
  { path: "message", after: "x".repeat(2000) },
];

describe.each(THEMES)("accessibility of the action proposal card, theme $name", (theme) => {
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

    it("has no WCAG 2.2 AA violations on a card in the queue with a change field by field", async () => {
      const { container } = renderWithProviders(<ProposalCard proposal={proposal({ target: "datadog:monitor/123", changes: CHANGES })} canDecide inQueue />);

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on a card that deletes", async () => {
      const { container } = renderWithProviders(<ProposalCard proposal={proposal({ target: "datadog:monitor/123", changes: [{ path: "", before: '{"name":"x"}' }] })} canDecide />);

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on an undo with its link", async () => {
      const { container } = renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", undoOf: PROPOSAL_ID, reason: "" })} canDecide />);

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the difference that a restore would overwrite", async () => {
      const user = userEvent.setup();
      const { api } = fakeServer(
        APPROVER,
        routes({
          [`POST /api/v1/proposals/${PROPOSAL_ID}/undo`]: () => json(409, { type: "about:blank", title: "Conflict", status: 409, code: "conflict", detail: "Changed.", changes: CHANGES }),
        }),
      );
      const { container } = renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", undoable: true, target: "datadog:monitor/123", changes: CHANGES })} canDecide />, { api });

      await user.click(screen.getByRole("button", { name: "Undo monitor-note" }));
      await user.click(screen.getByRole("button", { name: "Undo it" }));
      await screen.findByRole("button", { name: "Restore anyway" });

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});
