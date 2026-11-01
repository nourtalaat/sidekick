import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { VIEWER, renderApp } from "../test/helpers";
import { ADMIN } from "../test/rules";
import { sampleContent, strategyApi } from "../test/strategies";

describe("the page of one version", () => {
  it("shows who saved it, why, and its text, with links to the changes and back", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency/versions/1", handler });

    expect(await screen.findByRole("heading", { name: "checkout-latency, version 1", level: 1 })).toBeInTheDocument();
    expect(screen.getByText(/by Ad/)).toBeInTheDocument();
    expect(screen.getByText("Reason: First draft")).toBeInTheDocument();
    expect(screen.getByLabelText("Strategy YAML")).toHaveTextContent("description: Checkout is slow.");
    expect(screen.getByText(/A newer version exists/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Use this version as the base" })).toHaveAttribute("href", "/strategies/checkout-latency?base=1");
    expect(screen.queryByRole("link", { name: "Show what this version changed" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to checkout-latency" })).toHaveAttribute("href", "/strategies/checkout-latency");
  });

  it("links the changes of a later version, and does not offer the current one as a base", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency/versions/2", handler });

    expect(await screen.findByText(/This is the current version/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Show what this version changed" })).toHaveAttribute("href", "/strategies/checkout-latency/diff?from=1&to=2");
    expect(screen.queryByRole("link", { name: "Use this version as the base" })).not.toBeInTheDocument();
  });

  it("does not offer a base to a viewer", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: VIEWER, route: "/strategies/checkout-latency/versions/1", handler });

    await screen.findByRole("heading", { name: "checkout-latency, version 1", level: 1 });

    expect(screen.queryByRole("link", { name: "Use this version as the base" })).not.toBeInTheDocument();
  });

  it("says when the version does not exist", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency/versions/9", handler });

    expect(await screen.findByText("That version does not exist")).toBeInTheDocument();
  });

  it("says when the number is not a version number", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency/versions/abc", handler });

    expect(await screen.findByText("That version does not exist")).toBeInTheDocument();
  });
});
