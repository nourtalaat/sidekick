import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { sampleConfig } from "./test/config";
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

describe.each(THEMES)("accessibility of the Settings page, theme $name", (theme) => {
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

    it("has no WCAG 2.2 AA violations with every card closed", async () => {
      const { handler } = recording(() => json(200, sampleConfig()));
      const { container } = renderApp({ me: ADMIN, route: "/settings", handler });
      await screen.findByRole("heading", { name: "File settings" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations with every card open", async () => {
      const { handler } = recording(() => json(200, sampleConfig()));
      const { container } = renderApp({ me: ADMIN, route: "/settings", handler });
      await screen.findByRole("heading", { name: "File settings" });

      for (const card of Array.from(container.querySelectorAll("details"))) {
        card.setAttribute("open", "");
      }

      expect(container.querySelectorAll("details[open]")).toHaveLength(5);
      expect(container.querySelector('[role="group"][tabindex="0"]')).not.toBeNull();
      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations when the filter matches nothing", async () => {
      const { handler } = recording(() => json(200, sampleConfig()));
      const { container } = renderApp({ me: ADMIN, route: "/settings?q=zzz", handler });
      await screen.findByRole("heading", { name: "Nothing matches" });

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});
