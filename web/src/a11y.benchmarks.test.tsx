import { QueryClient } from "@tanstack/react-query";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { benchRunDetail, benchRunId, benchRunRange } from "./test/bench";
import { json, problem, renderApp, stubMedia, wcagViolations } from "./test/helpers";
import { recording } from "./test/rules";
import { THEME_STORAGE_KEY } from "./theme";

const NO_RUN = "No benchmark run has this ID.";

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

    it("has no WCAG 2.2 AA violations on the page of a failed run with a scenario open, and with a passed run", async () => {
      const user = userEvent.setup();
      const failed = benchRunDetail(2);
      const { container, unmount } = renderApp({ route: `/benchmarks/${failed.id}`, handler: () => json(200, failed) });
      await screen.findByRole("alert");

      expect(await wcagViolations(container)).toEqual([]);

      await user.click(screen.getByRole("button", { name: "oom-after-limit" }));
      await screen.findByRole("table", { name: "Repeats of oom-after-limit" });

      expect(await wcagViolations(container)).toEqual([]);

      unmount();

      const passed = benchRunDetail(1, { status: "passed", thresholds: [{ name: "verdictAccuracy", limit: 0.7, value: 0.75, passed: true }] });
      const second = renderApp({ route: `/benchmarks/${passed.id}`, handler: () => json(200, passed) });
      await screen.findByRole("heading", { name: "Cost and time", level: 2 });

      expect(await wcagViolations(second.container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations when no run has the ID, and when the run fails to load", async () => {
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const missing = renderApp({ route: `/benchmarks/${benchRunId(9)}`, handler: () => problem(404, NO_RUN) });
      await screen.findByRole("heading", { name: NO_RUN });

      expect(await wcagViolations(missing.container)).toEqual([]);

      missing.unmount();

      const broken = renderApp({ route: `/benchmarks/${benchRunId(9)}`, handler: () => problem(500, "The database is down."), queryClient });
      await screen.findByText("The database is down.");

      expect(await wcagViolations(broken.container)).toEqual([]);
    });
  });
});
