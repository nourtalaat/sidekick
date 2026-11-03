import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient } from "@tanstack/react-query";
import { useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { benchComparison, benchRuns, cost, emptyCost, emptyOperations, emptyQuality, FILTER_OPTIONS, operations, quality, RUN_A_ID, RUN_B_ID } from "../../test/stats";
import { fakeServer, json, problem, renderApp, renderWithProviders, type Handler } from "../../test/helpers";
import { routes } from "../../test/streams";
import { DashboardPage } from "./DashboardPage";

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

/** Renders the dashboard with the query string of the address on screen, to check what a choice writes to it. */
function renderDashboard(route: string, handler: Handler) {
  const Address = () => <output data-testid="address">{useLocation().search}</output>;

  return renderWithProviders(
    <>
      <DashboardPage />
      <Address />
    </>,
    { api: fakeServer(undefined, handler).api, route },
  );
}

async function cardOf(name: string): Promise<HTMLElement> {
  return (await screen.findByRole("heading", { name, level: 3 }, { timeout: LOAD_TIMEOUT })).closest("section") as HTMLElement;
}

/** The cells of the row of a day in the table view of a card. */
function cellsOf(card: HTMLElement, day: string): (string | null)[] {
  const row = within(card).getByRole("rowheader", { name: day }).closest("tr") as HTMLElement;

  return within(row)
    .getAllByRole("cell")
    .map((cell) => cell.textContent);
}

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

    // The counts are written as counts and only the accuracy as a percentage.
    const day = within(within(table).getByRole("rowheader", { name: "Oct 20" }).closest("tr") as HTMLElement);
    expect(day.getAllByRole("cell").map((cell) => cell.textContent)).toEqual(["85%", "13", "11"]);

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
    expect(within(screen.getByRole("search", { name: "Filter the dashboard" })).getByRole("combobox", { name: "Verdict" })).toHaveValue("");
  });

  it("says what the noisy alerts fixed chart counts", async () => {
    renderApp({ route: "/dashboard", handler: dashboard().handler });

    const card = (await screen.findByRole("heading", { name: "Noisy alerts fixed", level: 3 }, { timeout: LOAD_TIMEOUT })).closest("section") as HTMLElement;

    expect(within(card).getByText(/the fix was marked as applied, the monitor pull request that Sidekick opened was merged, or the monitor change that Sidekick made through the tool's API ran and was not undone/)).toBeInTheDocument();
    expect(within(card).getByText(/A false alarm counts once even when more than one applies\. 3 in all\./)).toBeInTheDocument();
  });

  it("says so, with a link to the filters, for a chart with nothing to draw", async () => {
    renderApp({ route: "/dashboard", handler: dashboard({ operations: () => json(200, operations({ series: operations().series.map((b) => ({ ...b, noisyFixed: 0 })), totals: { ...operations().totals, noisyFixed: 0 } })) }).handler });

    const card = (await screen.findByRole("heading", { name: "Noisy alerts fixed", level: 3 }, { timeout: LOAD_TIMEOUT })).closest("section") as HTMLElement;

    expect(within(card).getByText(/No data for this range/)).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: "Change the range" })).toHaveAttribute("href", "#dashboard-filters");
  });

  it("charts the precision and recall of false alarms by day, with a summary and a table", async () => {
    const user = userEvent.setup();
    renderApp({ route: "/dashboard", handler: dashboard().handler });

    const card = await cardOf("Precision and recall over time");

    expect(within(card).getByRole("combobox", { name: "Verdict" })).toHaveValue("false_positive");
    expect(within(within(card).getByRole("list", { name: "Series" })).getAllByRole("listitem").map((item) => item.textContent)).toEqual(["Precision", "Recall"]);
    expect(within(card).getByText("False alarm: precision 80% and recall 89% over the range, from 10 verdicts given and 9 confirmed.")).toBeInTheDocument();

    await user.click(within(card).getByRole("button", { name: "Show as table" }));

    const table = within(card).getByRole("table", { name: "Precision and recall over time, as a table" });

    expect(within(table).getAllByRole("columnheader").map((header) => header.textContent)).toEqual(["Day (UTC)", "Precision", "Recall", "Given", "Confirmed", "Agreed"]);
    expect(within(table).getAllByRole("row")).toHaveLength(31);
    expect(cellsOf(card, "Oct 20")).toEqual(["80%", "100%", "10", "8", "8"]);
    // A day with only one of the two numbers keeps it, and a day without the verdict has none.
    expect(cellsOf(card, "Oct 21")).toEqual(["none", "0%", "0", "1", "0"]);
    expect(cellsOf(card, "Oct 19")).toEqual(["none", "none", "0", "0", "0"]);
  });

  it("scores another verdict when it is chosen and keeps the choice in the address", async () => {
    const user = userEvent.setup();
    renderDashboard("/dashboard", dashboard().handler);

    const card = await cardOf("Precision and recall over time");

    expect(screen.getByTestId("address").textContent).toBe("");

    await user.selectOptions(within(card).getByRole("combobox", { name: "Verdict" }), "true_positive");

    expect(within(card).getByText("Real issue: precision 83% and recall 71% over the range, from 6 verdicts given and 7 confirmed.")).toBeInTheDocument();
    expect(screen.getByTestId("address")).toHaveTextContent("?scoreVerdict=true_positive");

    await user.click(within(card).getByRole("button", { name: "Show as table" }));

    expect(cellsOf(card, "Oct 20")).toEqual(["100%", "60%", "3", "5", "3"]);

    await user.selectOptions(within(card).getByRole("combobox", { name: "Verdict" }), "false_positive");

    expect(screen.getByTestId("address").textContent).toBe("");
  });

  it("starts from the verdict in the address and ignores one that is not a verdict", async () => {
    const { unmount } = renderApp({ route: "/dashboard?scoreVerdict=true_positive", handler: dashboard().handler });

    expect(within(await cardOf("Precision and recall over time")).getByRole("combobox", { name: "Verdict" })).toHaveValue("true_positive");

    unmount();
    renderApp({ route: "/dashboard?scoreVerdict=maybe", handler: dashboard().handler });

    expect(within(await cardOf("Precision and recall over time")).getByRole("combobox", { name: "Verdict" })).toHaveValue("false_positive");
  });

  it("keeps the score verdict apart from the verdict filter of the page", async () => {
    renderApp({ route: "/dashboard?scoreVerdict=flapping", handler: dashboard().handler });

    const card = await cardOf("Precision and recall over time");

    expect(within(card).getByRole("combobox", { name: "Verdict" })).toHaveValue("flapping");
    expect(within(screen.getByRole("search", { name: "Filter the dashboard" })).getByRole("combobox", { name: "Verdict" })).toHaveValue("");
  });

  it("says so, with a link to the filters, when no day has the verdict, and still offers the menu", async () => {
    const stats = quality({
      days: [{ day: "2026-10-20", rated: 3, correct: 3, scores: [{ verdict: "true_positive", predicted: 3, actual: 3, hits: 3, precision: 1, recall: 1 }] }],
      perVerdict: [{ verdict: "true_positive", predicted: 3, actual: 3, hits: 3, precision: 1, recall: 1 }],
    });
    renderApp({ route: "/dashboard", handler: dashboard({ quality: () => json(200, stats) }).handler });

    const card = await cardOf("Precision and recall over time");

    expect(within(card).getByText(/No data for this range/)).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: "Change the range" })).toHaveAttribute("href", "#dashboard-filters");
    expect(within(card).getByRole("combobox", { name: "Verdict" })).toHaveValue("false_positive");
    expect(within(card).queryByRole("button", { name: "Show as table" })).not.toBeInTheDocument();
  });

  it("draws precision only and says why when the page filters by verdict", async () => {
    const user = userEvent.setup();
    const filtered = quality({
      days: quality().days.map((d) => ({ ...d, scores: d.scores.filter((s) => s.verdict === "false_positive") })),
      perVerdict: quality().perVerdict.filter((s) => s.verdict === "false_positive"),
    });
    renderApp({ route: "/dashboard?verdict=false_positive&scoreVerdict=true_positive", handler: dashboard({ quality: () => json(200, filtered) }).handler });

    const card = await cardOf("Precision and recall over time");
    const note = "Recall needs every verdict. Clear the verdict filter to see it.";

    expect(within(card).getByText(note)).toBeInTheDocument();
    expect(within(card).queryByRole("combobox", { name: "Verdict" })).not.toBeInTheDocument();
    expect(within(card).getByText("False alarm: precision 80% over the range, from 10 verdicts given.")).toBeInTheDocument();

    await user.click(within(card).getByRole("button", { name: "Show as table" }));

    expect(within(within(card).getByRole("table")).getAllByRole("columnheader").map((header) => header.textContent)).toEqual(["Day (UTC)", "Precision", "Given", "Agreed"]);
    expect(cellsOf(card, "Oct 20")).toEqual(["80%", "10", "8"]);

    const scores = await screen.findByRole("region", { name: "Precision and recall of each verdict" });

    expect(screen.getAllByText(note)).toHaveLength(2);
    expect(within(scores).getByRole("columnheader", { name: "Precision" })).toBeInTheDocument();
    expect(within(scores).queryByRole("columnheader", { name: "Recall" })).not.toBeInTheDocument();
  });

  it("has no note and shows the recall series and column without the verdict filter", async () => {
    renderApp({ route: "/dashboard", handler: dashboard().handler });

    const card = await cardOf("Precision and recall over time");
    const scores = await screen.findByRole("region", { name: "Precision and recall of each verdict" });

    expect(screen.queryByText(/Recall needs every verdict/)).not.toBeInTheDocument();
    expect(within(card).getByText("Recall")).toBeInTheDocument();
    expect(within(scores).getByRole("columnheader", { name: "Recall" })).toBeInTheDocument();
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
    expect(within(runs).getByRole("link", { name: /^baseline, / })).toHaveAttribute("href", `/benchmarks/${RUN_A_ID}`);
    expect(within(runs).getByText("2 repeats, 4 runs")).toBeInTheDocument();
    expect(within(runs).getByText("Command line")).toBeInTheDocument();
    expect(within(runs).getByText("CI")).toBeInTheDocument();

    expect(screen.getByRole("link", { name: "All benchmark runs" })).toHaveAttribute("href", "/benchmarks");

    expect(screen.queryByRole("link", { name: "Open this comparison in Benchmarks" })).not.toBeInTheDocument();

    await user.selectOptions(screen.getByRole("combobox", { name: "Compare run" }), RUN_A_ID);

    expect(screen.queryByRole("link", { name: "Open this comparison in Benchmarks" })).not.toBeInTheDocument();

    await user.selectOptions(screen.getByRole("combobox", { name: "With run" }), RUN_B_ID);

    expect(screen.getByRole("link", { name: "Open this comparison in Benchmarks" })).toHaveAttribute("href", `/benchmarks/compare?a=${RUN_A_ID}&b=${RUN_B_ID}`);

    const metrics = await screen.findByRole("region", { name: /^Metrics of / });

    expect(within(metrics).getByText("Clear difference")).toBeInTheDocument();
    expect(within(metrics).getByText("No clear difference")).toBeInTheDocument();
    expect(within(metrics).getByText("-30 points")).toBeInTheDocument();
    expect(within(metrics).getByText("+$0.0020")).toBeInTheDocument();

    const scenarios = screen.getByRole("region", { name: /^Scenarios of / });

    expect(within(scenarios).getByText("2 of 2")).toBeInTheDocument();
    expect(within(scenarios).getAllByText("not run")).toHaveLength(4);
    expect(within(scenarios).getByText("Right to wrong")).toBeInTheDocument();

    const compare = urls.filter((url) => url.includes("/bench/compare")).at(-1) as string;
    expect(new URL(compare).searchParams.get("a")).toBe(RUN_A_ID);
    expect(new URL(compare).searchParams.get("b")).toBe(RUN_B_ID);
  });

  it("opens a comparison from the URL", async () => {
    renderApp({ route: `/dashboard?a=${RUN_A_ID}&b=${RUN_B_ID}`, handler: dashboard().handler });

    expect(await screen.findByRole("region", { name: /^Metrics of / }, { timeout: LOAD_TIMEOUT })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Compare run" })).toHaveValue(RUN_A_ID);
  });

  it("opens the same comparison on the Benchmarks compare page from the link of the card", async () => {
    const user = userEvent.setup();
    const { handler, urls } = dashboard();
    renderApp({ route: `/dashboard?a=${RUN_A_ID}&b=${RUN_B_ID}`, handler });

    await user.click(await screen.findByRole("link", { name: "Open this comparison in Benchmarks" }, { timeout: LOAD_TIMEOUT }));

    expect(await screen.findByRole("heading", { name: "Compare benchmark runs", level: 1 })).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: /^Metrics of / })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Run A" })).toHaveValue(RUN_A_ID);
    expect(screen.getByRole("combobox", { name: "Run B" })).toHaveValue(RUN_B_ID);

    const compare = urls.filter((url) => url.includes("/bench/compare")).at(-1) as string;
    expect(new URL(compare).searchParams.get("a")).toBe(RUN_A_ID);
    expect(new URL(compare).searchParams.get("b")).toBe(RUN_B_ID);
  });
});
