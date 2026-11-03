import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Button } from "./components/Button";
import { Card } from "./components/Card";
import { ProposalCard } from "./components/ProposalCard";
import { EmptyState } from "./components/EmptyState";
import { QueryError } from "./components/QueryError";
import { StatusBadge, VerdictBadge } from "./components/Badge";
import { TextField } from "./components/TextField";
import { ApiError } from "./api/client";
import { LoginPage } from "./pages/LoginPage";
import { THEME_STORAGE_KEY } from "./theme";
import { json, renderApp, renderWithProviders, stubMedia, VIEWER, wcagViolations } from "./test/helpers";
import { ci, proposal, pullRequest } from "./test/actionProposals";
import { ADMIN } from "./test/rules";
import { detail, INVESTIGATION_ID } from "./test/fixtures";
import { FakeEventSource, routes } from "./test/streams";
import type { UsedMemory } from "./api/investigations";

// The theme comes from the stored choice, or from the system when there is none.
const THEMES = [
  { name: "system, light", stored: undefined, dark: false },
  { name: "system, dark", stored: undefined, dark: true },
  { name: "light chosen", stored: "light", dark: true },
  { name: "dark chosen", stored: "dark", dark: false },
] as const;

// jsdom applies no CSS, so forced colors only changes what matchMedia answers.
// The CSS keeps borders and icons in that mode, and the badges carry both.
describe.each(THEMES)("accessibility, theme $name", (theme) => {
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

    it("has no WCAG 2.2 AA violations on the login page", async () => {
      const { container } = renderWithProviders(<LoginPage />, { route: "/login" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the shell", async () => {
      const { container } = renderApp({ route: "/investigations" });
      await screen.findByRole("heading", { name: "Investigations", level: 1 });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the shell when the theme saved on the server replaces the stored one", async () => {
      const saved = theme.stored === "dark" ? "light" : "dark";
      const { container } = renderApp({ me: { ...VIEWER, theme: saved }, route: "/investigations" });
      await screen.findByRole("heading", { name: "Investigations", level: 1 });

      await waitFor(() => expect(document.documentElement).toHaveAttribute("data-theme", saved));
      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the shell with a search in the header", async () => {
      const { container } = renderApp({ route: "/investigations?q=checkout" });
      await screen.findByRole("heading", { name: "Investigations", level: 1 });

      expect(screen.getByRole("searchbox", { name: "Search investigations" })).toHaveValue("checkout");
      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the shell with the menu of an admin", async () => {
      const { container } = renderApp({ me: ADMIN, route: "/investigations" });
      await screen.findByRole("link", { name: "Audit log" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the role message of a page that an admin only opens", async () => {
      const { container } = renderApp({ route: "/settings" });
      await screen.findByRole("heading", { name: "Needs the admin role" });

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the shell with the user menu open", async () => {
      const user = userEvent.setup();
      const { container } = renderApp({ route: "/investigations" });
      await user.click(await screen.findByRole("button", { name: /Ada Lovelace/ }));

      expect(await wcagViolations(container)).toEqual([]);
    });

    it("has no WCAG 2.2 AA violations on the investigation page with the memory card", async () => {
      vi.stubGlobal("EventSource", FakeEventSource);

      const path = `/api/v1/investigations/${INVESTIGATION_ID}`;
      const base = { investigationId: "0192d3a4-0000-7000-8000-0000000000e1", title: "Checkout p99", verdict: "true_positive", summary: "A deploy.", rank: 1, present: true, disabled: false } as const;
      const memoryExamples: UsedMemory[] = [
        { ...base, match: "fingerprint" },
        { ...base, investigationId: "e2", match: "service_alert", rank: 2, present: false, verdict: undefined, summary: undefined },
        { ...base, investigationId: "e3", match: "text", rank: 3, disabled: true },
        { ...base, investigationId: "e4", match: "similar", rank: 4 },
      ];
      const handler = routes({
        [`GET ${path}`]: () => json(200, detail({ memoryExamples })),
        [`GET ${path}/steps`]: () => json(200, { steps: [] }),
        [`GET ${path}/watchers`]: () => json(200, { watchers: [] }),
        [`GET ${path}/feedback`]: () => json(200, { parts: [] }),
      });
      const { container } = renderApp({ me: ADMIN, route: `/investigations/${INVESTIGATION_ID}`, handler });
      await screen.findByRole("region", { name: "Memory used" });

      expect(screen.getByRole("link", { name: "Manage memory" })).toBeInTheDocument();
      expect(await wcagViolations(container)).toEqual([]);

      vi.unstubAllGlobals();
    });

    it.each(["open", "merged", "closed"] as const)("has no WCAG 2.2 AA violations on a proposal card with a %s pull request", async (state) => {
      const { container } = renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest({ state }) })} canDecide={false} />);

      expect(await wcagViolations(container)).toEqual([]);
    });

    it.each(["pending", "passed", "failed", "none", "unknown", "timed_out"] as const)("has no WCAG 2.2 AA violations on a proposal card with %s CI", async (state) => {
      const over = {
        pending: { state, passed: 1, total: 3, reportedAt: undefined },
        passed: { state },
        failed: {
          state,
          passed: 5,
          total: 7,
          failedCount: 12,
          failed: [{ name: "unit tests", url: "https://ci.example.com/1" }, { name: "<b>lint</b>" }],
        },
        none: { state, passed: 0, total: 0 },
        unknown: { state, passed: 0, total: 0, message: "Sidekick could not read the checks. The GitHub App needs Checks (read)." },
        timed_out: { state, passed: 5, total: 7 },
      }[state];

      const { container } = renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest({ ci: ci(over) }) })} canDecide={false} />);

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});

describe("accessibility of the components", () => {
  it("has no WCAG 2.2 AA violations in the components that later pages use", async () => {
    const { container } = renderWithProviders(
      <main>
        <h1>Components</h1>
        <Button variant="primary">Primary</Button>
        <Button variant="secondary">Secondary</Button>
        <Button variant="destructive">Destructive</Button>
        <Button variant="brand">Brand</Button>
        <VerdictBadge verdict="true_positive" />
        <VerdictBadge verdict="known_issue" />
        <StatusBadge status="info" spinning>
          Running
        </StatusBadge>
        <Card title="A card">Content</Card>
        <TextField label="Name" help="As it shows in the audit log." />
        <TextField label="Email" error="Enter an email address." defaultValue="ada" />
        <EmptyState title="Nothing here yet">Nothing to show yet.</EmptyState>
        <QueryError error={new ApiError(403, "This needs the admin role.")} />
      </main>,
    );

    expect(await wcagViolations(container)).toEqual([]);
  });
});
