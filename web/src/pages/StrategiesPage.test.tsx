import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { VIEWER, json, renderApp } from "../test/helpers";
import { ADMIN, recording, rowAt } from "../test/rules";
import { sampleStrategies } from "../test/strategies";

describe("the strategies page", () => {
  it("lists every strategy with its status, where it is defined, its version and who changed it", async () => {
    const { handler } = recording(() => json(200, { strategies: sampleStrategies() }));
    renderApp({ me: VIEWER, route: "/strategies", handler });

    const table = await screen.findByRole("region", { name: "Strategies" });
    const rows = within(table).getAllByRole("row");

    expect(rows).toHaveLength(6);

    const checkout = rowAt(rows, 1);
    expect(within(checkout).getByRole("link", { name: "checkout-latency" })).toHaveAttribute("href", "/strategies/checkout-latency");
    expect(within(checkout).getByText("Active")).toBeInTheDocument();
    expect(within(checkout).getByText("Added at runtime")).toBeInTheDocument();
    expect(within(checkout).getByText("runtime:2")).toBeInTheDocument();
    expect(within(checkout).getByText("by Ad")).toBeInTheDocument();

    const builtIn = rowAt(rows, 2);
    expect(within(builtIn).getByText("Built in")).toBeInTheDocument();
    expect(within(builtIn).getByText("0123456789abcdef")).toBeInTheDocument();
    expect(within(builtIn).getByText("Not changed here")).toBeInTheDocument();

    const file = rowAt(rows, 3);
    expect(within(file).getByText("Strategy file")).toBeInTheDocument();
    expect(within(file).getByText("Replaces the runtime strategy")).toBeInTheDocument();

    const shadowed = rowAt(rows, 4);
    expect(within(shadowed).getByText("Shadowed")).toBeInTheDocument();
    expect(within(shadowed).getByText("a strategy file has the same name, and is used instead")).toBeInTheDocument();

    const broken = rowAt(rows, 5);
    expect(within(broken).getByText("Invalid")).toBeInTheDocument();
    expect(within(broken).getByText("description: required")).toBeInTheDocument();
  });

  it("is in the navigation", async () => {
    const { handler } = recording(() => json(200, { strategies: sampleStrategies() }));
    renderApp({ me: ADMIN, route: "/strategies", handler });

    await screen.findByRole("region", { name: "Strategies" });

    expect(within(screen.getByRole("navigation", { name: "Main" })).getByRole("link", { name: "Strategies" })).toHaveAttribute("href", "/strategies");
  });

  it("says why the list cannot be read", async () => {
    const { handler } = recording(() => json(403, { type: "about:blank", title: "Forbidden", status: 403, detail: "This needs the viewer role." }));
    renderApp({ me: VIEWER, route: "/strategies", handler });

    expect(await screen.findByText("Needs the viewer role")).toBeInTheDocument();
  });
});
