import { QueryClient } from "@tanstack/react-query";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { benchRunRange } from "./test/bench";
import { json, problem, renderApp, stubMedia, wcagViolations } from "./test/helpers";
import { recording } from "./test/rules";
import { THEME_STORAGE_KEY } from "./theme";

// The same four ways to get a theme as the other accessibility tests.
const THEMES = [
  { name: "system, light", stored: undefined, dark: false },
  { name: "system, dark", stored: undefined, dark: true },
  { name: "light chosen", stored: "light", dark: true },
  { name: "dark chosen", stored: "dark", dark: false },
] as const;

const NEXT = "cursor-of-page-one";

describe.each(THEMES)("accessibility of the Benchmarks page, theme $name", (theme) => {
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

    it("has no WCAG 2.2 AA violations with a page of runs and Load more, and after the next page", async () => {
      const user = userEvent.setup();
      const { handler } = recording((request) => (request.search.get("cursor") === NEXT ? json(200, { runs: benchRunRange(4, 5) }) : json(200, { runs: benchRunRange(1, 3), nextCursor: NEXT })));
      const { container } = renderApp({ route: "/benchmarks", handler });
      await screen.findByRole("button", { name: "Load more" });

      expect(await wcagViolations(container)).toEqual([]);

      await user.click(screen.getByRole("button", { name: "Load more" }));
      await screen.findByText("Showing 5 runs");

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations when there are no runs", async () => {
      const { container } = renderApp({ route: "/benchmarks", handler: () => json(200, { runs: [] }) });
      await screen.findByRole("heading", { name: "No benchmark runs yet" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations when the list fails", async () => {
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const { container } = renderApp({ route: "/benchmarks", handler: () => problem(500, "The database is down."), queryClient });
      await screen.findByText("The database is down.");

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});
