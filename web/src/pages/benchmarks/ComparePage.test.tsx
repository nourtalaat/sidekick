import { QueryClient } from "@tanstack/react-query";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { benchRunDetailOf, compareAnswers, type CompareAnswers } from "../../test/bench";
import { json, problem, renderApp } from "../../test/helpers";
import { recording } from "../../test/rules";
import { benchComparison, benchRuns, RUN_A_ID, RUN_B_ID } from "../../test/stats";

const BOTH = `/benchmarks/compare?a=${RUN_A_ID}&b=${RUN_B_ID}`;
const NO_RUN = "No benchmark run has this ID.";

function noRetries(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function renderCompare(route: string, answers: CompareAnswers = {}) {
  const { handler, seen } = recording(compareAnswers(answers));

  return { ...renderApp({ route, handler, queryClient: noRetries() }), seen };
}

function comparesOf(seen: { path: string; search: URLSearchParams }[]) {
  return seen.filter((request) => request.path === "/api/v1/bench/compare");
}

function rowOf(name: RegExp): HTMLElement {
  return within(screen.getByRole("region", { name: "Settings of Run A and Run B" })).getByRole("row", { name });
}

describe("the compare page, choosing runs", () => {
  it("compares the two runs of the address, asks for the newest 50 runs for its menus and shows both menus with their runs chosen", async () => {
    const { seen } = renderCompare(BOTH);

    expect(await screen.findByRole("heading", { name: "Compare benchmark runs", level: 1 })).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: /^Metrics of / })).toBeInTheDocument();

    const lists = seen.filter((request) => request.path === "/api/v1/bench/runs");
    expect(lists[0]?.search.get("limit")).toBe("50");
    expect(comparesOf(seen)[0]?.search.get("a")).toBe(RUN_A_ID);
    expect(comparesOf(seen)[0]?.search.get("b")).toBe(RUN_B_ID);
    expect(screen.getByRole("combobox", { name: "Run A" })).toHaveValue(RUN_A_ID);
    expect(screen.getByRole("combobox", { name: "Run B" })).toHaveValue(RUN_B_ID);
    expect(within(screen.getByRole("combobox", { name: "Run A" })).getByRole("option", { name: /^baseline, / })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "All benchmark runs" })).toHaveAttribute("href", "/benchmarks");
  });

  it("shows a run that is not among the newest 50, named by its ID, and still compares it", async () => {
    const [newest] = benchRuns();
    const { seen } = renderCompare(BOTH, { runs: [newest as NonNullable<typeof newest>] });

    expect(await screen.findByRole("region", { name: /^Metrics of / })).toBeInTheDocument();

    const menu = screen.getByRole("combobox", { name: "Run A" });

    expect(menu).toHaveValue(RUN_A_ID);
    expect(within(menu).getByRole("option", { name: RUN_A_ID })).toBeInTheDocument();
    expect(comparesOf(seen)[0]?.search.get("a")).toBe(RUN_A_ID);
  });

  it("compares as soon as the second run is chosen, and sends no comparison request before", async () => {
    const user = userEvent.setup();
    const { seen } = renderCompare("/benchmarks/compare");

    await screen.findAllByRole("option", { name: /^baseline, / });
    await user.selectOptions(screen.getByRole("combobox", { name: "Run A" }), RUN_A_ID);

    expect(screen.getByText("Choose Run B to compare it with Run A.")).toBeInTheDocument();
    expect(comparesOf(seen)).toHaveLength(0);

    await user.selectOptions(screen.getByRole("combobox", { name: "Run B" }), RUN_B_ID);

    expect(await screen.findByRole("region", { name: /^Metrics of / })).toBeInTheDocument();
    expect(comparesOf(seen).at(-1)?.search.get("a")).toBe(RUN_A_ID);
    expect(comparesOf(seen).at(-1)?.search.get("b")).toBe(RUN_B_ID);
    expect(screen.queryByText(/^Choose Run /)).not.toBeInTheDocument();
  });

  it("swaps the two runs", async () => {
    const user = userEvent.setup();
    const { seen } = renderCompare(BOTH);
    await screen.findByRole("region", { name: /^Metrics of / });

    await user.click(screen.getByRole("button", { name: "Swap runs" }));

    await waitFor(() => expect(comparesOf(seen).at(-1)?.search.get("a")).toBe(RUN_B_ID));

    expect(comparesOf(seen).at(-1)?.search.get("b")).toBe(RUN_A_ID);
    expect(screen.getByRole("combobox", { name: "Run A" })).toHaveValue(RUN_B_ID);
    expect(screen.getByRole("combobox", { name: "Run B" })).toHaveValue(RUN_A_ID);
  });

  it("moves the only chosen run to the other menu when swapped", async () => {
    const user = userEvent.setup();
    renderCompare(`/benchmarks/compare?a=${RUN_A_ID}`);
    await screen.findByText("Choose Run B to compare it with Run A.");

    await user.click(screen.getByRole("button", { name: "Swap runs" }));

    expect(await screen.findByText("Choose Run A to compare it with Run B.")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Run B" })).toHaveValue(RUN_A_ID);
  });
});

describe("the compare page, with one run or none chosen", () => {
  it.each([
    ["no run", "/benchmarks/compare", "Choose two runs to compare them."],
    ["only run A", `/benchmarks/compare?a=${RUN_A_ID}`, "Choose Run B to compare it with Run A."],
    ["only run B", `/benchmarks/compare?b=${RUN_B_ID}`, "Choose Run A to compare it with Run B."],
    ["a value that is not a run ID, which counts as no run", "/benchmarks/compare?a=nope&b=also-not", "Choose two runs to compare them."],
  ])("says what to choose with %s, and sends no request for a comparison", async (_name, route, words) => {
    const { seen } = renderCompare(route);

    expect(await screen.findByText(words)).toBeInTheDocument();
    await screen.findAllByRole("option", { name: /^baseline, / });

    expect(comparesOf(seen)).toHaveLength(0);
    expect(screen.queryByRole("region", { name: /^Metrics of / })).not.toBeInTheDocument();
  });

  it("says there are no runs when there are none and none is chosen", async () => {
    renderCompare("/benchmarks/compare", { runs: [] });

    expect(await screen.findByRole("heading", { name: "No benchmark runs yet" })).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Run A" })).not.toBeInTheDocument();
  });

  it("keeps the menus working from the address when the list of runs fails, and offers a retry", async () => {
    const user = userEvent.setup();
    let failing = true;
    renderCompare(`/benchmarks/compare?a=${RUN_A_ID}`, { list: () => (failing ? problem(500, "The database is down.") : json(200, { runs: benchRuns() })) });

    expect(await screen.findByText("The database is down.")).toBeInTheDocument();
    expect(screen.getByText("Choose Run B to compare it with Run A.")).toBeInTheDocument();
    expect(within(screen.getByRole("combobox", { name: "Run A" })).getByRole("option", { name: RUN_A_ID })).toBeInTheDocument();

    failing = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(await within(screen.getByRole("combobox", { name: "Run A" })).findByRole("option", { name: /^baseline, / })).toBeInTheDocument();
    expect(screen.queryByText("The database is down.")).not.toBeInTheDocument();
  });
});

describe("the compare page, when the comparison fails", () => {
  it.each([
    ["an unknown ID", 404, NO_RUN],
    ["a request the API refuses", 400, "a must be a run ID."],
  ])("shows the message of the API for %s", async (_name, status, detail) => {
    renderCompare(BOTH, { compare: () => problem(status, detail) });

    expect(await screen.findByText(detail)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: /^Metrics of / })).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Run A" })).toHaveValue(RUN_A_ID);
  });
});

describe("the compare page, settings", () => {
  function differing(): CompareAnswers {
    const comparison = benchComparison();
    const judge = { connector: "claude", model: "judge-model" };
    comparison.b = { ...comparison.b, connectors: { ...comparison.b.connectors, judge } };

    return {
      compare: () => json(200, comparison),
      details: {
        [RUN_A_ID]: () => json(200, benchRunDetailOf(comparison.a)),
        [RUN_B_ID]: () => json(200, benchRunDetailOf(comparison.b, { promptVersion: "p-4", configHash: "00ff11aa22bb" })),
      },
    };
  }

  it("shows a row for each setting of the two runs, and says Differs in words for each row where they differ", async () => {
    renderCompare(BOTH, differing());
    await screen.findByText("p-4");

    expect(rowOf(/^Label/)).toHaveTextContent("Labelbaselinenone");
    expect(rowOf(/^Label/)).toHaveTextContent("Differs");
    expect(rowOf(/^Sidekick version/)).toHaveTextContent("Sidekick versionv0.9.0v0.9.1Differs");
    expect(rowOf(/^Git commit/)).toHaveTextContent("Git commitabc1234def5678Differs");
    expect(rowOf(/^Scenario set/)).toHaveTextContent("Scenario setstarterstarterSame");
    expect(rowOf(/^Repeats/)).toHaveTextContent("Repeats21Differs");
    expect(rowOf(/^Model for investigate/)).toHaveTextContent("big-model through claude (anthropic)small-model through openaiDiffers");
    expect(rowOf(/^Model for judge/)).toHaveTextContent("not usedjudge-model through claudeDiffers");
    expect(rowOf(/^Strategy generic-alert version/)).toHaveTextContent("version v1not usedDiffers");
    expect(rowOf(/^Prompt version/)).toHaveTextContent("Prompt versionp-3p-4Differs");
    expect(rowOf(/^Judge prompt version/)).toHaveTextContent("Judge prompt versionj-2j-2Same");
    expect(rowOf(/^Config hash/)).toHaveTextContent("9f2c1d7e5a6b00ff11aa22bbDiffers");
    expect(rowOf(/^Started/)).toHaveTextContent("Not compared");
    expect(rowOf(/^Started/)).not.toHaveTextContent("Differs");
  });

  it("marks a differing row with a style as well as the word, and leaves a row that is the same without it", async () => {
    renderCompare(BOTH, differing());
    await screen.findByText("p-4");

    expect(rowOf(/^Sidekick version/)).toHaveClass("sk-compare__row--differs");
    expect(rowOf(/^Scenario set/)).not.toHaveClass("sk-compare__row--differs");
    expect(within(rowOf(/^Sidekick version/)).getByText("Differs").querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  });

  it("shows no differences when the settings are the same", async () => {
    const comparison = benchComparison();
    comparison.b = { ...comparison.a };
    renderCompare(BOTH, { compare: () => json(200, comparison) });
    await screen.findByRole("region", { name: "Settings of Run A and Run B" });

    await waitFor(() => expect(rowOf(/^Config hash/)).toHaveTextContent("Same"));

    expect(within(screen.getByRole("region", { name: "Settings of Run A and Run B" })).queryByText("Differs", { selector: "span" })).not.toBeInTheDocument();
  });

  it("says Loading for the settings of a run detail that has not come yet, and still shows the rest", async () => {
    renderCompare(BOTH, { details: { [RUN_A_ID]: () => new Promise<Response>(() => undefined) } });

    expect(await screen.findByRole("region", { name: /^Metrics of / })).toBeInTheDocument();

    await waitFor(() => expect(rowOf(/^Prompt version/)).toHaveTextContent("Loadingp-3Not compared yet"));


    expect(rowOf(/^Config hash/)).toHaveTextContent("Loading");
    expect(rowOf(/^Sidekick version/)).toHaveTextContent("v0.9.0v0.9.1Differs");
    expect(screen.getByRole("region", { name: /^Scenarios of / })).toBeInTheDocument();
  });

  it("says Not available when a run detail fails, keeps the rest and retries both on Try again", async () => {
    const user = userEvent.setup();
    let failing = true;
    const { seen } = renderCompare(BOTH, {
      details: { [RUN_B_ID]: () => (failing ? problem(500, "The database is down.") : json(200, benchRunDetailOf(benchComparison().b))) },
    });

    expect(await screen.findByText("The database is down.")).toBeInTheDocument();

    expect(rowOf(/^Prompt version/)).toHaveTextContent("p-3Not availableNot compared");
    expect(rowOf(/^Sidekick version/)).toHaveTextContent("Differs");
    expect(screen.getByRole("region", { name: /^Metrics of / })).toBeInTheDocument();

    failing = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() => expect(rowOf(/^Prompt version/)).toHaveTextContent("p-3p-3Same"));

    expect(seen.filter((request) => request.path === `/api/v1/bench/runs/${RUN_B_ID}`).length).toBeGreaterThan(1);
    expect(screen.queryByText("The database is down.")).not.toBeInTheDocument();
  });
});

describe("the compare page, scores and scenarios", () => {
  it("shows the metric table with its caption and units, and the scenarios with the one that went from right to wrong first", async () => {
    renderCompare(BOTH);

    const scores = await screen.findByRole("region", { name: /^Metrics of / });

    expect(within(scores).getByText(/A difference is clear when the confidence intervals of the two runs do not overlap\./)).toBeInTheDocument();
    expect(within(scores).getByText("-30 points")).toBeInTheDocument();
    expect(within(scores).getByText("+$0.0020")).toBeInTheDocument();
    expect(within(scores).getByText("Clear difference")).toBeInTheDocument();

    const rows = within(screen.getByRole("region", { name: /^Scenarios of / })).getAllByRole("row");

    expect(rows[1]).toHaveTextContent("s1");
    expect(within(rows[1] as HTMLElement).getByText("Right to wrong")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Scores", level: 2 })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Scenarios", level: 2 })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Settings", level: 2 })).toBeInTheDocument();
  });
});
