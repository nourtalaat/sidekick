import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { benchComparison, benchRuns, cost, emptyCost, emptyOperations, emptyQuality, FILTER_OPTIONS, operations, quality, RUN_A_ID, RUN_B_ID } from "../../test/stats";
import { json, problem, renderApp, type Handler } from "../../test/helpers";
import { routes } from "../../test/streams";

type Answer = () => Response;

interface Answers {
  quality?: Answer;
  cost?: Answer;
  operations?: Answer;
}

/** A fake server for the dashboard that records the URL of every request. */
function dashboard(answers: Answers = {}): { handler: Handler; urls: string[] } {
  const urls: string[] = [];
  const record = (answer: Answer) => (request: Request) => {
    urls.push(request.url);

    return answer();
  };

  const handler = routes({
    "GET /api/v1/stats/filters": () => json(200, FILTER_OPTIONS),
    "GET /api/v1/stats/quality": record(answers.quality ?? (() => json(200, quality()))),
    "GET /api/v1/stats/cost": record(answers.cost ?? (() => json(200, cost()))),
    "GET /api/v1/stats/operations": record(answers.operations ?? (() => json(200, operations()))),
    "GET /api/v1/bench/runs": () => json(200, { runs: benchRuns() }),
    "GET /api/v1/bench/compare": record(() => json(200, benchComparison())),
  });

  return { handler, urls };
}

const LOAD_TIMEOUT = 10_000;

// The page counts its ranges from the day it opens, and the fixtures are of late October 2026.
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-10-28T12:00:00Z"));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("the dashboard", () => {
  it("shows the key numbers with their change in words", async () => {
    renderApp({ route: "/dashboard", handler: dashboard().handler });

    const tiles = await screen.findByRole("region", { name: "Key numbers" }, { timeout: LOAD_TIMEOUT });

    await waitFor(() => expect(within(tiles).getByText("76%")).toBeInTheDocument());
    expect(within(tiles).getByText("Up 1 point, from 17 rated")).toBeInTheDocument();

    expect(within(tiles).getByText("False alarms found")).toBeInTheDocument();
    expect(within(tiles).getByText("14")).toBeInTheDocument();
    expect(within(tiles).getByText("Up 180 percent")).toBeInTheDocument();

    expect(within(tiles).getByText("about 47 s")).toBeInTheDocument();
    expect(within(tiles).getByText("Up 19 s")).toBeInTheDocument();

    expect(within(tiles).getByText("$0.0850 of $0.1000")).toBeInTheDocument();
    expect(within(tiles).getByText("85% of the cap, up $0.0690 from yesterday")).toBeInTheDocument();
  });

  it("has a section for quality, cost and operations", async () => {
    renderApp({ route: "/dashboard", handler: dashboard().handler });

    for (const name of ["Quality", "Cost", "Operations"]) {
      expect(await screen.findByRole("heading", { name, level: 2 }, { timeout: LOAD_TIMEOUT })).toBeInTheDocument();
    }

    for (const name of ["Verdict accuracy over time", "Spend per day by model", "Investigations per day by verdict", "Benchmark runs"]) {
      expect(await screen.findByRole("heading", { name, level: 3 })).toBeInTheDocument();
    }
  });

  it("shows the confusion matrix with a word for each cell that is not empty", async () => {
    renderApp({ route: "/dashboard", handler: dashboard().handler });

    const matrix = await screen.findByRole("region", { name: "Counts by verdict given and confirmed" }, { timeout: LOAD_TIMEOUT });

    expect(within(matrix).getAllByText("agrees")).toHaveLength(2);
    expect(within(matrix).getAllByText("differs")).toHaveLength(2);
    expect(within(matrix).getByText("rated wrong")).toBeInTheDocument();
    expect(within(matrix).getByRole("columnheader", { name: "Unknown" })).toBeInTheDocument();
  });

  it("shows precision and recall of each verdict", async () => {
    renderApp({ route: "/dashboard", handler: dashboard().handler });

    const table = await screen.findByRole("region", { name: "Precision and recall of each verdict" }, { timeout: LOAD_TIMEOUT });
    const rows = within(table).getAllByRole("row");

    expect(rows).toHaveLength(3);
    expect(within(rows[1] as HTMLElement).getByText("Real issue")).toBeInTheDocument();
    expect(within(rows[1] as HTMLElement).getByText("83%")).toBeInTheDocument();
    expect(within(rows[2] as HTMLElement).getByText("False alarm")).toBeInTheDocument();
    expect(within(rows[2] as HTMLElement).getByText("80%")).toBeInTheDocument();
  });

  it("shows a status next to the daily cap and a route over its own", async () => {
    renderApp({ route: "/dashboard", handler: dashboard().handler });

    expect(await screen.findByText("Near daily cap", undefined, { timeout: LOAD_TIMEOUT })).toBeInTheDocument();

    const today = await screen.findByRole("region", { name: "Spend and cap of each route today" });

    expect(within(today).getByText("Over the daily cap")).toBeInTheDocument();
    expect(within(today).getByText("No cap set")).toBeInTheDocument();
  });

  it("shows the numbers of a chart as a table and back", async () => {
    const user = userEvent.setup();
    renderApp({ route: "/dashboard", handler: dashboard().handler });

    const card = (await screen.findByRole("heading", { name: "Verdict accuracy over time", level: 3 }, { timeout: LOAD_TIMEOUT })).closest("section") as HTMLElement;

    await user.click(within(card).getByRole("button", { name: "Show as table" }));

    const table = within(card).getByRole("table", { name: "Verdict accuracy over time, as a table" });

    expect(within(table).getByRole("columnheader", { name: "Confirmed as given" })).toBeInTheDocument();
    // One row for each day of the range, 30 in all, and the header.
    expect(within(table).getAllByRole("row")).toHaveLength(31);

    await user.click(within(card).getByRole("button", { name: "Show as chart" }));

    expect(within(card).queryByRole("table")).not.toBeInTheDocument();
  });

  it("names the series of a chart in a legend and says what it shows in words", async () => {
    renderApp({ route: "/dashboard", handler: dashboard().handler });

    const card = (await screen.findByRole("heading", { name: "Investigations per day by verdict", level: 3 }, { timeout: LOAD_TIMEOUT })).closest("section") as HTMLElement;
    const legend = within(card).getByRole("list", { name: "Series" });

    expect(within(legend).getByText("Duplicate or known issue")).toBeInTheDocument();
    expect(within(card).getByText(/23 investigations were created, 22 of them ended/)).toBeInTheDocument();
  });

  it("keeps the filters in the URL and sends them with every request", async () => {
    const user = userEvent.setup();
    const { handler, urls } = dashboard();
    renderApp({ route: "/dashboard?range=7d&route=payments&verdict=false_positive", handler });

    await waitFor(() => expect(urls.length).toBeGreaterThanOrEqual(3), { timeout: LOAD_TIMEOUT });

    const first = new URL(urls[0] as string);
    expect(first.searchParams.get("route")).toBe("payments");
    expect(first.searchParams.get("verdict")).toBe("false_positive");
    expect(first.searchParams.get("from")).toMatch(/^\d{4}-\d{2}-\d{2}$/);

    expect(screen.getByRole("combobox", { name: "Time range" })).toHaveValue("7d");
    expect(screen.getByRole("combobox", { name: "Route" })).toHaveValue("payments");

    urls.length = 0;

    await screen.findByRole("option", { name: "checkout-api" });
    await user.selectOptions(screen.getByRole("combobox", { name: "Service" }), "checkout-api");

    await waitFor(() => expect(urls.length).toBeGreaterThanOrEqual(3));
    expect(new URL(urls.at(-1) as string).searchParams.get("service")).toBe("checkout-api");
    expect(new URL(urls.at(-1) as string).searchParams.get("route")).toBe("payments");
  });

  it("starts from custom dates and sends them", async () => {
    const user = userEvent.setup();
    const { handler, urls } = dashboard();
    renderApp({ route: "/dashboard", handler });

    await user.selectOptions(await screen.findByRole("combobox", { name: "Time range" }), "custom");

    const from = await screen.findByLabelText("From (UTC)");
    expect(screen.getByLabelText("To (UTC)")).toHaveValue("2026-10-28");

    fireEvent.change(from, { target: { value: "2026-10-01" } });

    await waitFor(() => expect(new URL(urls.at(-1) as string).searchParams.get("from")).toBe("2026-10-01"));
    expect(new URL(urls.at(-1) as string).searchParams.get("to")).toBe("2026-10-28");
  });

  it("clears the filters", async () => {
    const user = userEvent.setup();
    renderApp({ route: "/dashboard?range=7d&route=payments", handler: dashboard().handler });

    await user.click(await screen.findByRole("button", { name: "Clear" }));

    expect(screen.getByRole("combobox", { name: "Time range" })).toHaveValue("30d");
    expect(screen.getByRole("combobox", { name: "Route" })).toHaveValue("");
  });

  it("falls back to the default range for a URL the API would refuse", async () => {
    renderApp({ route: "/dashboard?range=custom&from=nonsense&to=2026-10-01&verdict=maybe", handler: dashboard().handler });

    expect(await screen.findByRole("combobox", { name: "Time range" })).toHaveValue("30d");
    expect(screen.getByRole("combobox", { name: "Verdict" })).toHaveValue("");
  });

  it("says so, with a link to the filters, for a chart with nothing to draw", async () => {
    renderApp({ route: "/dashboard", handler: dashboard({ operations: () => json(200, operations({ series: operations().series.map((b) => ({ ...b, noisyFixed: 0 })), totals: { ...operations().totals, noisyFixed: 0 } })) }).handler });

    const card = (await screen.findByRole("heading", { name: "Noisy alerts fixed", level: 3 }, { timeout: LOAD_TIMEOUT })).closest("section") as HTMLElement;

    expect(within(card).getByText(/No data for this range/)).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: "Change the range" })).toHaveAttribute("href", "#dashboard-filters");
  });

  it("shows the empty state when the whole dashboard is empty", async () => {
    renderApp({
      route: "/dashboard",
      handler: dashboard({ quality: () => json(200, emptyQuality()), cost: () => json(200, emptyCost()), operations: () => json(200, emptyOperations()) }).handler,
    });

    expect(await screen.findByRole("heading", { name: "Nothing to show yet" }, { timeout: LOAD_TIMEOUT })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Quality" })).not.toBeInTheDocument();
  });

  it("shows an error in the section that failed and keeps the others", async () => {
    renderApp({
      route: "/dashboard",
      handler: dashboard({ operations: () => problem(500, "The database is down.") }).handler,
      queryClient: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    });

    expect(await screen.findByRole("heading", { name: "Verdict accuracy over time", level: 3 }, { timeout: LOAD_TIMEOUT })).toBeInTheDocument();
    expect(await screen.findByText("The database is down.", undefined, { timeout: LOAD_TIMEOUT })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("explains which filters the spend cannot use", async () => {
    renderApp({ route: "/dashboard?service=checkout-api", handler: dashboard({ cost: () => json(200, cost({ ignoredFilters: ["service"] })) }).handler });

    expect(await screen.findByText(/cannot be filtered by service; only the cost per investigation uses it/, undefined, { timeout: LOAD_TIMEOUT })).toBeInTheDocument();
  });

  it("lists the benchmark runs and compares two of them", async () => {
    const user = userEvent.setup();
    const { handler, urls } = dashboard();
    renderApp({ route: "/dashboard", handler });

    const runs = await screen.findByRole("region", { name: "Recent benchmark runs" }, { timeout: LOAD_TIMEOUT });

    expect(within(runs).getByText("Passed")).toBeInTheDocument();
    expect(within(runs).getByText("Failed")).toBeInTheDocument();
    expect(within(runs).getByText("70%")).toBeInTheDocument();

    await user.selectOptions(screen.getByRole("combobox", { name: "Compare run" }), RUN_A_ID);
    await user.selectOptions(screen.getByRole("combobox", { name: "With run" }), RUN_B_ID);

    const metrics = await screen.findByRole("region", { name: /^Metrics of / });

    expect(within(metrics).getByText("Clear difference")).toBeInTheDocument();
    expect(within(metrics).getByText("No clear difference")).toBeInTheDocument();
    expect(within(metrics).getByText("-30 points")).toBeInTheDocument();
    expect(within(metrics).getByText("+$0.0020")).toBeInTheDocument();

    const scenarios = screen.getByRole("region", { name: /^Scenarios of / });

    expect(within(scenarios).getByText("2 of 2")).toBeInTheDocument();
    expect(within(scenarios).getAllByText("not run")).toHaveLength(2);

    const compare = urls.filter((url) => url.includes("/bench/compare")).at(-1) as string;
    expect(new URL(compare).searchParams.get("a")).toBe(RUN_A_ID);
    expect(new URL(compare).searchParams.get("b")).toBe(RUN_B_ID);
  });

  it("opens a comparison from the URL", async () => {
    renderApp({ route: `/dashboard?a=${RUN_A_ID}&b=${RUN_B_ID}`, handler: dashboard().handler });

    expect(await screen.findByRole("region", { name: /^Metrics of / }, { timeout: LOAD_TIMEOUT })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Compare run" })).toHaveValue(RUN_A_ID);
  });
});
