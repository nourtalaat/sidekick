import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { VIEWER, renderApp } from "../test/helpers";
import { sampleContent, strategyApi } from "../test/strategies";

describe("the diff page", () => {
  it("shows the lines that changed between two versions, with who saved each", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: VIEWER, route: "/strategies/checkout-latency/diff?from=1&to=2", handler });

    expect(await screen.findByRole("heading", { name: "Changes in checkout-latency, version 1 to version 2", level: 1 })).toBeInTheDocument();
    expect(await screen.findByText("2 lines added, 0 lines removed.")).toBeInTheDocument();
    expect(screen.getByText(/Version 1, saved .* by Ad: First draft/)).toBeInTheDocument();
    expect(screen.getByText(/Version 2, saved .* by Ad: Add the first check/)).toBeInTheDocument();

    const diff = screen.getByRole("region", { name: "Changes from version 1 to version 2" });
    const rows = within(diff).getAllByRole("row");

    // The hidden header row, then the two lines both versions share and the two lines version 2 adds.
    expect(rows).toHaveLength(5);
    expect(within(rows[3] as HTMLElement).getByText("Added:")).toBeInTheDocument();
    expect(rows[3]).toHaveTextContent("first:");
    expect(rows[4]).toHaveTextContent("- Check what changed.");
  });

  it("shows removed lines going back", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: VIEWER, route: "/strategies/checkout-latency/diff?from=2&to=1", handler });

    expect(await screen.findByText("0 lines added, 2 lines removed.")).toBeInTheDocument();
    expect(screen.getAllByText("Removed:")).toHaveLength(2);
  });

  it("says when the two versions are the same", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: VIEWER, route: "/strategies/checkout-latency/diff?from=2&to=2", handler });

    expect(await screen.findByText("The two texts are the same.")).toBeInTheDocument();
  });

  it("asks for two versions when the address names none", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: VIEWER, route: "/strategies/checkout-latency/diff", handler });

    expect(await screen.findByText("Pick two versions")).toBeInTheDocument();
  });

  it("says when a version does not exist", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: VIEWER, route: "/strategies/checkout-latency/diff?from=1&to=7", handler });

    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });
});
