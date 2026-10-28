import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { detail, INVESTIGATION_ID, RESPONDER, step, summary, watcher } from "./test/fixtures";
import { json, renderApp, stubMedia, wcagViolations } from "./test/helpers";
import { FakeEventSource, routes } from "./test/streams";
import { THEME_STORAGE_KEY } from "./theme";

// The same four theme cases, with and without forced colors, as the other pages.
const THEMES = [
  { name: "system, light", stored: undefined, dark: false },
  { name: "system, dark", stored: undefined, dark: true },
  { name: "light chosen", stored: "light", dark: true },
  { name: "dark chosen", stored: "dark", dark: false },
] as const;

const PATH = `/api/v1/investigations/${INVESTIGATION_ID}`;

const handler = routes({
  "GET /api/v1/investigations": () =>
    json(200, {
      investigations: [
        summary(),
        summary({ id: "inv-2", title: "Disk almost full", verdict: "false_positive", feedback: { correct: 0, wrong: 1, summary: "wrong" }, watcher: { name: "health", status: "running" } }),
        summary({ id: "inv-3", title: undefined, verdict: undefined, status: "running", watcher: undefined, feedback: { correct: 0, wrong: 0, summary: "none" } }),
      ],
      nextCursor: "next",
    }),
  [`GET ${PATH}`]: () => json(200, detail({ pendingFollowUps: [{ text: "Was it the cache?", author: "Ana", createdAt: "2026-10-26T15:00:00Z" }] })),
  [`GET ${PATH}/steps`]: () =>
    json(200, {
      steps: [
        step(1, { parts: [{ type: "tool_call", id: "c1", name: "search_logs", input: { query: "errors" } }] }),
        step(2, { kind: "tool_result", tool: "search_logs", source: "signoz", parts: [{ type: "tool_result", callId: "c1", content: "3 errors" }] }),
      ],
    }),
  [`GET ${PATH}/watchers`]: () =>
    json(200, {
      watchers: [
        watcher(),
        watcher({ id: "w2", name: "disk", status: "recovered", endedAt: "2026-10-26T14:30:00Z" }),
        watcher({ id: "w3", name: "queue", status: "timed_out", endedAt: "2026-10-26T15:30:00Z" }),
        watcher({ id: "w4", name: "cache", status: "stopped", stopReason: "responder", endedAt: "2026-10-26T14:26:00Z", stoppedBy: { name: "Grace Hopper" } }),
      ],
    }),
});

beforeEach(() => {
  FakeEventSource.reset();
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe.each(THEMES)("accessibility of the investigation pages, theme $name", (theme) => {
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
      const { container } = renderApp({ route: "/investigations", handler });
      await screen.findByRole("table");

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the page of a responder, with a step open", async () => {
      const user = userEvent.setup();
      const { container } = renderApp({ me: RESPONDER, route: `/investigations/${INVESTIGATION_ID}`, handler });
      await user.click(await screen.findByRole("button", { name: /Model turn: called search_logs/ }));
      await screen.findByText(/Checking: health/);
      expect(screen.getByRole("form", { name: "Ask a follow-up" })).toBeInTheDocument();

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the page of a viewer", async () => {
      const { container } = renderApp({ route: `/investigations/${INVESTIGATION_ID}`, handler });
      await screen.findByRole("button", { name: /Model turn: called search_logs/ });
      await screen.findByText(/Checking: health/);

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});
