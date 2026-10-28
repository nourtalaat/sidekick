import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { renderApp } from "../test/helpers";

// The chart library loads on demand, which takes a moment in a cold test run.
const LOAD_TIMEOUT = 10_000;

describe("the dashboard", () => {
  it("shows the sample chart with a legend and a table view of the same numbers", async () => {
    const user = userEvent.setup();
    renderApp({ route: "/dashboard" });

    const toggle = await screen.findByRole("button", { name: "Show as table" }, { timeout: LOAD_TIMEOUT });

    expect(screen.getByRole("heading", { name: "Investigations per day by verdict", level: 2 })).toBeInTheDocument();
    expect(screen.getByText("Duplicate or known issue")).toBeInTheDocument();

    await user.click(toggle);

    const table = screen.getByRole("table", { name: /Investigations per day by verdict/ });
    expect(within(table).getAllByRole("row")).toHaveLength(15);
    expect(within(table).getByRole("columnheader", { name: "Known issue" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Show as chart" }));

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  }, 20_000);
});
