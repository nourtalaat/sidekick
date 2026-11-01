import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { VIEWER, json, renderApp } from "../../test/helpers";
import { ADMIN, recording } from "../../test/rules";

describe("the review queue", () => {
  it("shows the scenarios tab to an admin, with the tab in the address", async () => {
    const { handler, seen } = recording(() => json(200, { scenarios: [] }));
    renderApp({ me: ADMIN, route: "/review?tab=scenarios", handler });

    expect(await screen.findByRole("heading", { name: "Review queue", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Scenarios" })).toHaveAttribute("aria-current", "page");
    expect(await screen.findByRole("heading", { name: "No scenarios yet" })).toBeInTheDocument();
    expect(seen.some((r) => r.path === "/api/v1/scenarios")).toBe(true);
  });

  it("opens the first tab when the address names none or an unknown one", async () => {
    const { handler } = recording(() => json(200, { scenarios: [] }));
    renderApp({ me: ADMIN, route: "/review?tab=nothing", handler });

    expect(await screen.findByRole("heading", { name: "Benchmark scenarios" })).toBeInTheDocument();
  });

  it("tells a viewer that nothing is for their role, and asks for no scenarios", async () => {
    const { handler, seen } = recording(() => json(200, { scenarios: [] }));
    renderApp({ me: VIEWER, route: "/review", handler });

    expect(await screen.findByRole("heading", { name: "Nothing for your role to review" })).toBeInTheDocument();
    expect(seen.some((r) => r.path === "/api/v1/scenarios")).toBe(false);
  });
});
