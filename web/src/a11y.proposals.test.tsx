import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { THEME_STORAGE_KEY } from "./theme";
import { renderApp, stubMedia, wcagViolations } from "./test/helpers";
import { failedProposal, passedProposal, proposalApi } from "./test/proposals";
import { ADMIN } from "./test/rules";

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
