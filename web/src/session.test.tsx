import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "./api/client";
import { QueryError } from "./components/QueryError";
import { NAV_ITEMS } from "./shell/Nav";
import { problem, renderApp } from "./test/helpers";

describe("the signed-in app", () => {
  it("shows the shell with its navigation, and the empty state of a page that is not built yet", async () => {
    renderApp({ route: "/lessons" });

    expect(await screen.findByRole("heading", { name: "Lessons", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("Comes in a later release.")).toBeInTheDocument();

    const nav = screen.getByRole("navigation", { name: "Main" });
    for (const { label } of NAV_ITEMS) {
      expect(within(nav).getByRole("link", { name: label })).toBeInTheDocument();
    }

    expect(within(nav).getByRole("link", { name: "Lessons" })).toHaveAttribute("aria-current", "page");
  });

  it("opens Investigations from the root", async () => {
    renderApp({ route: "/" });

    expect(await screen.findByRole("heading", { name: "Investigations", level: 1 })).toBeInTheDocument();
  });

  it("has a search box that is off until the investigation list lands", async () => {
    renderApp({ route: "/investigations" });

    const search = await screen.findByRole("searchbox", { name: "Search investigations" });

    expect(search).toBeDisabled();
    expect(search).toHaveAccessibleDescription("Search comes with the investigation list.");
  });

  it("shows who is signed in, and their role, in the user menu", async () => {
    const user = userEvent.setup();
    renderApp({ route: "/investigations" });

    await user.click(await screen.findByRole("button", { name: /Ada Lovelace/ }));

    expect(screen.getByText("Role: viewer")).toBeInTheDocument();
  });
});

describe("a missing session", () => {
  it("sends the visitor to the login page, which signs in and comes back to the page", async () => {
    renderApp({ me: null, route: "/lessons?tab=open" });

    expect(await screen.findByRole("heading", { name: "Sidekick", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/auth/login?return=%2Flessons%3Ftab%3Dopen");
  });

  it("sends the browser to the login page when a later API call answers 401", async () => {
    const { api } = renderApp({
      route: "/lessons",
      handler: () => problem(401, "Sign in to continue."),
    });
    await screen.findByRole("heading", { name: "Lessons", level: 1 });

    await act(async () => {
      await api.client.GET("/tokens");
    });

    expect(await screen.findByRole("heading", { name: "Sidekick", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/auth/login?return=%2Flessons");
  });

  it("does not let the return parameter leave the site", async () => {
    renderApp({ me: null, route: "/login?return=https://evil.example/" });

    expect(await screen.findByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/auth/login?return=%2F");
  });
});

describe("sign out", () => {
  it("sends the CSRF token and returns to the login page", async () => {
    const user = userEvent.setup();
    const { seen } = renderApp({ route: "/lessons" });

    await user.click(await screen.findByRole("button", { name: /Ada Lovelace/ }));
    await user.click(screen.getByRole("button", { name: "Sign out" }));

    expect(await screen.findByRole("heading", { name: "Sidekick", level: 1 })).toBeInTheDocument();

    const logout = seen.find((request) => request.path === "/auth/logout");
    expect(logout?.method).toBe("POST");
    expect(logout?.headers.get("X-CSRF-Token")).toBe("csrf-ada");
  });
});

describe("QueryError", () => {
  it("names the role that a 403 asks for", () => {
    render(<QueryError error={new ApiError(403, "This needs the admin role.")} />);

    expect(screen.getByRole("alert")).toHaveTextContent("Needs the admin role");
  });

  it("offers a retry for other failures", async () => {
    const user = userEvent.setup();
    const retry = vi.fn();
    render(<QueryError error={new ApiError(500, "Something failed on the server.")} onRetry={retry} />);

    expect(screen.getByRole("alert")).toHaveTextContent("Something failed on the server.");

    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(retry).toHaveBeenCalledOnce();
  });
});
