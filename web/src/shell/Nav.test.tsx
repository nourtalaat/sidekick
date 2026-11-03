import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Me } from "../session";
import { VIEWER, renderApp } from "../test/helpers";
import { ADMIN, RESPONDER } from "../test/rules";
import { NAV_ITEMS } from "./Nav";

const APPROVER: Me = { ...VIEWER, role: "approver" };

const VIEWER_ITEMS = ["Investigations", "Dashboard", "Review queue", "Lessons", "Routes", "Strategies", "Listeners", "Benchmarks"];
const ADMIN_ITEMS = ["Audit log", "Settings"];

describe("the menu", () => {
  it.each([
    { role: "viewer", me: VIEWER, shown: VIEWER_ITEMS },
    { role: "responder", me: RESPONDER, shown: VIEWER_ITEMS },
    { role: "approver", me: APPROVER, shown: VIEWER_ITEMS },
    { role: "admin", me: ADMIN, shown: [...VIEWER_ITEMS, ...ADMIN_ITEMS] },
  ])("shows a $role these items, in this order", async ({ me, shown }) => {
    renderApp({ me, route: "/investigations" });

    const nav = await screen.findByRole("navigation", { name: "Main" });

    expect(within(nav).getAllByRole("link").map((link) => link.textContent)).toEqual(shown);
  });

  it("has no item that the two lists above leave out", () => {
    expect(NAV_ITEMS.map(({ label }) => label).sort()).toEqual([...VIEWER_ITEMS, ...ADMIN_ITEMS].sort());
  });

  it.each(NAV_ITEMS.filter(({ minRole }) => minRole === "admin"))("shows a viewer the role message at $to, the address of an item that only an admin sees", async ({ to, label }) => {
    renderApp({ me: VIEWER, route: to });

    expect(await screen.findByRole("heading", { name: label, level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Needs the admin role" })).toBeInTheDocument();
  });
});
