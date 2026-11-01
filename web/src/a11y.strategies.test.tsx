import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { THEME_STORAGE_KEY } from "./theme";
import { VIEWER, renderApp, stubMedia, wcagViolations } from "./test/helpers";
import { ADMIN, validationProblem } from "./test/rules";
import { CHECKOUT_V2, sampleContent, strategyApi } from "./test/strategies";

// The same four ways to get a theme as the other accessibility tests.
const THEMES = [
  { name: "system, light", stored: undefined, dark: false },
  { name: "system, dark", stored: undefined, dark: true },
  { name: "light chosen", stored: "light", dark: true },
  { name: "dark chosen", stored: "dark", dark: false },
] as const;

describe.each(THEMES)("accessibility of the strategy pages, theme $name", (theme) => {
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

    it("has no WCAG 2.2 AA violations on the strategies page", async () => {
      const { handler } = strategyApi(sampleContent());
      const { container } = renderApp({ me: VIEWER, route: "/strategies", handler });
      await screen.findByRole("region", { name: "Strategies" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the page of a runtime strategy for an admin", async () => {
      const { handler } = strategyApi(sampleContent());
      const { container } = renderApp({ me: ADMIN, route: "/strategies/checkout-latency", handler });
      await screen.findByRole("region", { name: "Versions of checkout-latency" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the page of a runtime strategy for a viewer", async () => {
      const { handler } = strategyApi(sampleContent());
      const { container } = renderApp({ me: VIEWER, route: "/strategies/checkout-latency", handler });
      await screen.findByRole("region", { name: "Versions of checkout-latency" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the editor with problems and the changes showing", async () => {
      const user = userEvent.setup();
      const content = sampleContent();
      content.save = () =>
        validationProblem([
          { field: "reason", message: "a reason is required" },
          { field: "body.match.textPattern", message: "invalid regular expression (line 4)" },
        ]);

      const { handler } = strategyApi(content);
      const { container } = renderApp({ me: ADMIN, route: "/strategies/checkout-latency", handler });

      const editor = await screen.findByLabelText("Strategy (YAML)");
      await user.clear(editor);
      await user.click(editor);
      await user.paste(`${CHECKOUT_V2}budgets: {}\n`);
      await user.click(screen.getByRole("button", { name: "Show my changes" }));
      await user.click(screen.getByRole("button", { name: "Save as a new version" }));
      await screen.findByText("match.textPattern: invalid regular expression (line 4)");

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the page of a built-in strategy", async () => {
      const { handler } = strategyApi(sampleContent());
      const { container } = renderApp({ me: ADMIN, route: "/strategies/generic-alert", handler });
      await screen.findByRole("button", { name: "Edit as runtime strategy" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the page of one version", async () => {
      const { handler } = strategyApi(sampleContent());
      const { container } = renderApp({ me: ADMIN, route: "/strategies/checkout-latency/versions/1", handler });
      await screen.findByLabelText("Strategy YAML");

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the diff page", async () => {
      const { handler } = strategyApi(sampleContent());
      const { container } = renderApp({ me: VIEWER, route: "/strategies/checkout-latency/diff?from=1&to=2", handler });
      await screen.findByRole("region", { name: "Changes from version 1 to version 2" });

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});
