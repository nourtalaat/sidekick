import { QueryClient } from "@tanstack/react-query";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { benchRun, benchRunId, benchRunRange } from "../../test/bench";
import { json, problem, renderApp } from "../../test/helpers";
import { ADMIN, recording } from "../../test/rules";

const NEXT = "cursor-of-page-one";
const FIRST_PAGE = 25;
const ALL = 28;

function noRetries(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

/** Serves 25 runs, then 3 more once the cursor of the first page comes back. */
function twoPages() {
  return recording((request) =>
    request.search.get("cursor") === NEXT ? json(200, { runs: benchRunRange(FIRST_PAGE + 1, ALL) }) : json(200, { runs: benchRunRange(1, FIRST_PAGE), nextCursor: NEXT }),
  );
}

describe("the Benchmarks page", () => {
  it("lists a page of 25 runs and loads the next page with the cursor when Load more is pressed", async () => {
    const user = userEvent.setup();
    const { handler, seen } = twoPages();
    renderApp({ route: "/benchmarks", handler });

    const table = await screen.findByRole("region", { name: "Benchmark runs" });

    expect(within(table).getAllByRole("row")).toHaveLength(FIRST_PAGE + 1);
    expect(screen.getByRole("status")).toHaveTextContent("Showing 25 runs");
    expect(seen.at(-1)?.search.get("limit")).toBe("25");
    expect(seen.at(-1)?.search.get("cursor")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Load more" }));

    expect(await screen.findByText("Showing 28 runs")).toBeInTheDocument();
    expect(within(table).getAllByRole("row")).toHaveLength(ALL + 1);
    expect(seen.at(-1)?.search.get("cursor")).toBe(NEXT);
    expect(seen.at(-1)?.search.get("limit")).toBe("25");
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("shows each result as a word with an icon, and each run name as a link to its page", async () => {
    const { handler } = recording(() => json(200, { runs: [benchRun(1, { label: "baseline" }), benchRun(2)] }));
    renderApp({ route: "/benchmarks", handler });

    const table = await screen.findByRole("region", { name: "Benchmark runs" });

    for (const word of ["Passed", "Failed"]) {
      expect(within(table).getByText(word).querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    }

    expect(within(table).getByRole("link", { name: /^baseline, / })).toHaveAttribute("href", `/benchmarks/${benchRunId(1)}`);
    expect(within(table).getByRole("link", { name: /^Run of / })).toHaveAttribute("href", `/benchmarks/${benchRunId(2)}`);
  });

  it("says that there are no runs yet and what stores them", async () => {
    renderApp({ route: "/benchmarks", handler: () => json(200, { runs: [] }) });

    expect(await screen.findByRole("heading", { name: "No benchmark runs yet" })).toBeInTheDocument();
    expect(screen.getByText("Runs appear here when sidekick bench run stores its results with --db.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Benchmark runs" })).not.toBeInTheDocument();
  });

  it("shows why the list failed and loads it again when asked", async () => {
    const user = userEvent.setup();
    let fail = true;
    renderApp({ route: "/benchmarks", handler: () => (fail ? problem(500, "The database is down.") : json(200, { runs: [benchRun(1)] })), queryClient: noRetries() });

    expect(await screen.findByText("The database is down.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Benchmark runs" })).not.toBeInTheDocument();

    fail = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("region", { name: "Benchmark runs" })).toBeInTheDocument();
    expect(screen.queryByText("The database is down.")).not.toBeInTheDocument();
  });

  it("keeps the runs it has when the next page fails, and tries that page again when asked", async () => {
    const user = userEvent.setup();
    let fail = true;
    const { handler } = recording((request) => {
      if (request.search.get("cursor") === NEXT) {
        return fail ? problem(500, "The database is down.") : json(200, { runs: benchRunRange(FIRST_PAGE + 1, ALL) });
      }

      return json(200, { runs: benchRunRange(1, FIRST_PAGE), nextCursor: NEXT });
    });
    renderApp({ route: "/benchmarks", handler, queryClient: noRetries() });

    await user.click(await screen.findByRole("button", { name: "Load more" }));

    expect(await screen.findByText("The database is down.")).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "Benchmark runs" })).getAllByRole("row")).toHaveLength(FIRST_PAGE + 1);

    fail = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByText("Showing 28 runs")).toBeInTheDocument();
    expect(screen.queryByText("The database is down.")).not.toBeInTheDocument();
  });

  it.each([
    { name: "a viewer", me: undefined },
    { name: "an admin", me: ADMIN },
  ])("opens for $name", async ({ me }) => {
    renderApp({ me, route: "/benchmarks", handler: () => json(200, { runs: [benchRun(1)] }) });

    expect(await screen.findByRole("heading", { name: "Benchmarks", level: 1 })).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: "Benchmark runs" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Needs the admin role" })).not.toBeInTheDocument();
  });
});
