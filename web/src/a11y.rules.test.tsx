import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { THEME_STORAGE_KEY } from "./theme";
import { VIEWER, json, renderApp, stubMedia, wcagViolations } from "./test/helpers";
import { ADMIN, listener, recording, route, sampleListeners, sampleRoutes, validationProblem } from "./test/rules";

// The same four ways to get a theme as the other accessibility tests.
const THEMES = [
  { name: "system, light", stored: undefined, dark: false },
  { name: "system, dark", stored: undefined, dark: true },
  { name: "light chosen", stored: "light", dark: true },
  { name: "dark chosen", stored: "dark", dark: false },
] as const;

const INVESTIGATION = "01920000-0000-7000-8000-0000000000a1";

const AUDIT_ENTRIES = [
  {
    id: 2,
    at: "2026-10-20T10:05:00Z",
    actorKind: "user",
    actorId: "01920000-0000-7000-8000-000000000040",
    actorName: "Ad",
    action: "route.updated",
    targetKind: "route",
    targetId: "checkout",
    old: { priority: 20 },
    new: { priority: 5 },
  },
  { id: 1, at: "2026-10-20T10:00:00Z", actorKind: "policy", action: "action.executed", targetKind: "proposal", targetId: "p1", investigationId: INVESTIGATION },
];

describe.each(THEMES)("accessibility of the rule pages, theme $name", (theme) => {
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

    it("has no WCAG 2.2 AA violations on the listeners page", async () => {
      const { handler } = recording(() => json(200, { listeners: sampleListeners() }));
      const { container } = renderApp({ me: ADMIN, route: "/listeners", handler });
      await screen.findByRole("region", { name: "Listeners" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the listeners page while a delete is confirmed", async () => {
      const user = userEvent.setup();
      const { handler } = recording(() => json(200, { listeners: sampleListeners() }));
      const { container } = renderApp({ me: ADMIN, route: "/listeners", handler });
      await user.click(await screen.findByRole("button", { name: "Delete listener ops" }));

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the routes page", async () => {
      const { handler } = recording(() => json(200, { routes: sampleRoutes() }));
      const { container } = renderApp({ me: ADMIN, route: "/routes", handler });
      await screen.findByRole("region", { name: "Routes" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the routes page for a viewer", async () => {
      const { handler } = recording(() => json(200, { routes: sampleRoutes() }));
      const { container } = renderApp({ me: VIEWER, route: "/routes", handler });
      await screen.findByRole("region", { name: "Routes" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the listener form with errors showing", async () => {
      const user = userEvent.setup();
      const { handler } = recording(() => json(200, { listeners: [] }));
      const { container } = renderApp({ me: ADMIN, route: "/listeners/new", handler });
      await user.click(await screen.findByRole("button", { name: "Add listener" }));
      await screen.findByText("Enter a channel.");

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the listener form of a listener", async () => {
      const { handler } = recording(() => json(200, { listeners: [listener()] }));
      const { container } = renderApp({ me: ADMIN, route: "/listeners/ops", handler });
      await screen.findByLabelText("In use");

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the route form with errors from the server", async () => {
      const user = userEvent.setup();

      const { handler } = recording((request) =>
        request.method === "POST"
          ? validationProblem([
              { field: "budgets.maxToolCalls", message: "100 is above the global ceiling" },
              { field: "match", message: "required: a runtime route needs a match" },
              { field: "other", message: "is not a field of the form" },
            ])
          : json(200, { routes: [] }),
      );

      const { container } = renderApp({ me: ADMIN, route: "/routes/new", handler });
      await user.type(await screen.findByLabelText("Name"), "search");
      await user.click(screen.getByRole("button", { name: "Add route" }));
      await screen.findByText("required: a runtime route needs a match");

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the form of a route", async () => {
      const { handler } = recording(() => json(200, { routes: [route()] }));
      const { container } = renderApp({ me: ADMIN, route: "/routes/checkout", handler });
      await screen.findByLabelText("In use");

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the audit log with a change shown", async () => {
      const user = userEvent.setup();
      const { handler } = recording(() => json(200, { entries: AUDIT_ENTRIES, nextCursor: "more" }));
      const { container } = renderApp({ me: ADMIN, route: "/audit", handler });
      const table = await screen.findByRole("region", { name: "Audit log" });
      await user.click(within(table).getByText(/Show change/));

      expect(screen.getByRole("button", { name: "Show older entries" })).toBeInTheDocument();
      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the empty audit log", async () => {
      const { handler } = recording(() => json(200, { entries: [] }));
      const { container } = renderApp({ me: ADMIN, route: "/audit?actor=nobody", handler });
      await screen.findByRole("heading", { name: "No entries match these filters" });

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});
