import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { Button } from "./components/Button";
import { Card } from "./components/Card";
import { EmptyState } from "./components/EmptyState";
import { QueryError } from "./components/QueryError";
import { StatusBadge, VerdictBadge } from "./components/Badge";
import { TextField } from "./components/TextField";
import { ApiError } from "./api/client";
import { LoginPage } from "./pages/LoginPage";
import { THEME_STORAGE_KEY } from "./theme";
import { renderApp, renderWithProviders, stubMedia, wcagViolations } from "./test/helpers";

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

    it("has no WCAG 2.2 AA violations on the shell with the user menu open", async () => {
      const user = userEvent.setup();
      const { container } = renderApp({ route: "/investigations" });
      await user.click(await screen.findByRole("button", { name: /Ada Lovelace/ }));

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
        <EmptyState title="Nothing here yet">Comes in a later release.</EmptyState>
        <QueryError error={new ApiError(403, "This needs the admin role.")} />
      </main>,
    );

    expect(await wcagViolations(container)).toEqual([]);
  });
});
