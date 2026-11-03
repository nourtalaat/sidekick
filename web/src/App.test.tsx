import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Me } from "./session";
import { VIEWER, json, renderApp } from "./test/helpers";
import { ADMIN, RESPONDER } from "./test/rules";

const APPROVER: Me = { ...VIEWER, role: "approver" };

const ADMIN_PAGES = [
  { route: "/audit", title: "Audit log" },
  { route: "/settings", title: "Settings" },
] as const;

const BELOW_ADMIN = [
  { name: "viewer", me: VIEWER },
  { name: "responder", me: RESPONDER },
  { name: "approver", me: APPROVER },
] as const;

describe.each(ADMIN_PAGES)("the $title page by address", ({ route, title }) => {
  it.each(BELOW_ADMIN)("tells a $name that it needs the admin role, and sends no request but the sign-in check", async ({ me }) => {
    const { seen } = renderApp({ me, route, handler: () => json(200, { entries: [] }) });

    expect(await screen.findByRole("heading", { name: title, level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Needs the admin role" })).toBeInTheDocument();
    expect(seen.map((request) => request.path)).toEqual(["/api/v1/me"]);
  });
});

describe("the pages that need no more than the viewer role", () => {
  it.each([
    { route: "/dashboard", title: "Dashboard" },
    { route: "/benchmarks", title: "Benchmarks" },
  ])("opens $route for a viewer", async ({ route, title }) => {
    renderApp({ me: VIEWER, route });

    expect(await screen.findByRole("heading", { name: title, level: 1 })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Needs the admin role" })).not.toBeInTheDocument();
  });
});

describe("the pages of an admin", () => {
  it("opens Settings", async () => {
    renderApp({ me: ADMIN, route: "/settings" });

    expect(await screen.findByRole("heading", { name: "Settings", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("Comes in a later release.")).toBeInTheDocument();
  });

  it("opens the audit log and asks for its entries", async () => {
    const { seen } = renderApp({ me: ADMIN, route: "/audit", handler: () => json(200, { entries: [] }) });

    expect(await screen.findByRole("heading", { name: "Nothing recorded yet" })).toBeInTheDocument();
    expect(seen.some((request) => request.path === "/api/v1/audit")).toBe(true);
  });
});
