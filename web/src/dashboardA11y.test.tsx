import { QueryClient } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { benchComparison, benchRuns, cost, emptyCost, emptyOperations, emptyQuality, FILTER_OPTIONS, operations, quality, RUN_A_ID, RUN_B_ID } from "./test/stats";
import { json, problem, renderApp, stubMedia, wcagViolations, type Handler } from "./test/helpers";
import { routes } from "./test/streams";
import { THEME_STORAGE_KEY } from "./theme";

// The same four theme cases, with and without forced colors, as the other pages.
const THEMES = [
  { name: "system, light", stored: undefined, dark: false },
  { name: "system, dark", stored: undefined, dark: true },
  { name: "light chosen", stored: "light", dark: true },
  { name: "dark chosen", stored: "dark", dark: false },
] as const;

const LOAD_TIMEOUT = 10_000;
const TEST_TIMEOUT = 30_000;

function server(data: "data" | "empty" | "operations fails"): Handler {
  const empty = data === "empty";

  return routes({
    "GET /api/v1/stats/filters": () => json(200, FILTER_OPTIONS),
    "GET /api/v1/stats/quality": () => json(200, empty ? emptyQuality() : quality()),
    "GET /api/v1/stats/cost": () => json(200, empty ? emptyCost() : cost({ ignoredFilters: ["service"] })),
    "GET /api/v1/stats/operations": () => (data === "operations fails" ? problem(500, "The database is down.") : json(200, empty ? emptyOperations() : operations())),
    "GET /api/v1/bench/runs": () => json(200, { runs: benchRuns() }),
    "GET /api/v1/bench/compare": () => json(200, benchComparison()),
  });
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-10-28T12:00:00Z"));
});

afterEach(() => {
  vi.useRealTimers();
});

describe.each(THEMES)("accessibility of the dashboard, theme $name", (theme) => {
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

    it(
      "has no WCAG 2.2 AA violations with data, a chart drawn and a comparison open",
      async () => {
        const { container } = renderApp({ route: `/dashboard?a=${RUN_A_ID}&b=${RUN_B_ID}&service=checkout-api`, handler: server("data") });

        await screen.findByRole("region", { name: /^Metrics of / }, { timeout: LOAD_TIMEOUT });
        await waitFor(() => expect(container.querySelector(".recharts-surface")).not.toBeNull(), { timeout: LOAD_TIMEOUT });

        expect(await wcagViolations(container)).toEqual([]);
      },
      TEST_TIMEOUT,
    );

    it(
      "has no WCAG 2.2 AA violations with every chart as a table",
      async () => {
        const user = userEvent.setup();
        const { container } = renderApp({ route: "/dashboard", handler: server("data") });

        await screen.findByRole("heading", { name: "Noisy alerts fixed", level: 3 }, { timeout: LOAD_TIMEOUT });

        for (const button of screen.getAllByRole("button", { name: "Show as table" })) {
          await user.click(button);
        }

        expect(await wcagViolations(container)).toEqual([]);
      },
      TEST_TIMEOUT,
    );

    it(
      "has no WCAG 2.2 AA violations when there is nothing to show",
      async () => {
        const { container } = renderApp({ route: "/dashboard", handler: server("empty") });

        await screen.findByRole("heading", { name: "Nothing to show yet" }, { timeout: LOAD_TIMEOUT });

        expect(await wcagViolations(container)).toEqual([]);
      },
      TEST_TIMEOUT,
    );

    it(
      "has no WCAG 2.2 AA violations when a section fails",
      async () => {
        const { container } = renderApp({
          route: "/dashboard",
          handler: server("operations fails"),
          queryClient: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
        });

        await screen.findByText("The database is down.", undefined, { timeout: LOAD_TIMEOUT });

        expect(await wcagViolations(container)).toEqual([]);
      },
      TEST_TIMEOUT,
    );
  });
});
