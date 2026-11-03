import { QueryClient } from "@tanstack/react-query";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { BenchRun } from "../../api/bench";
import { formatTime } from "../../format";
import { benchResult, benchRunDetail, benchRunId } from "../../test/bench";
import { json, problem, renderApp } from "../../test/helpers";
import { recording } from "../../test/rules";
import { nameOf } from "./RunsTable";

const NO_RUN = "No benchmark run has this ID.";

function renderRun(run: BenchRun, options: { queryClient?: QueryClient } = {}) {
  const { handler, seen } = recording(() => json(200, run));

  return { ...renderApp({ route: `/benchmarks/${run.id}`, handler, queryClient: options.queryClient }), runs: seen };
}

/** The value of a fact of a list, found by its name. */
function factOf(name: string): HTMLElement {
  return screen.getByText(name, { selector: "dt" }).nextElementSibling as HTMLElement;
}

function rowOf(table: HTMLElement, name: RegExp | string): HTMLElement {
  return within(table).getByRole("row", { name });
}

describe("the page of one benchmark run, header", () => {
  it("asks for the run by its ID and shows its title, its result and the facts it was made with", async () => {
    const run = benchRunDetail(2, { strategies: { "generic-alert": "3", "latency-alert": "1" }, connectors: { judge: { connector: "claude", provider: "anthropic", model: "judge-model" }, investigate: { connector: "claude", model: "big-model" } } });
    const { runs } = renderRun(run);

    expect(await screen.findByRole("heading", { name: nameOf(run), level: 1 })).toBeInTheDocument();
    expect(runs.map((r) => r.path)).toContain(`/api/v1/bench/runs/${run.id}`);
    expect(within(screen.getByRole("heading", { name: nameOf(run), level: 1 }).parentElement as HTMLElement).getByText("Failed")).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "Thresholds of this run" })).getAllByText("Failed")).toHaveLength(2);
    expect(factOf("Started")).toHaveTextContent(formatTime(run.startedAt));
    expect(factOf("Finished")).toHaveTextContent(formatTime(run.finishedAt));
    expect(factOf("Sidekick version")).toHaveTextContent("v0.9.1");
    expect(factOf("Git commit")).toHaveTextContent("abc1234");
    expect(factOf("Started by")).toHaveTextContent("Command line");
    expect(factOf("Scenario set")).toHaveTextContent("starter, 2 repeats");
    expect(factOf("Models")).toHaveTextContent("investigate: big-model through claudejudge: judge-model through claude (anthropic)");
    expect(factOf("Strategies")).toHaveTextContent("generic-alert, version 3latency-alert, version 1");
    expect(factOf("Prompt version")).toHaveTextContent("p-3");
    expect(factOf("Judge prompt version")).toHaveTextContent("j-2");
    expect(factOf("Config hash")).toHaveTextContent("9f2c1d7e5a6b");
  });

  it("shows the result of a passed run as a word with an icon, uses the label as the title, and says none for no strategies", async () => {
    const run = benchRunDetail(1, { label: "nightly", status: "passed", thresholds: [] });
    renderRun(run);

    const heading = await screen.findByRole("heading", { name: nameOf(run), level: 1 });
    const badge = within(heading.parentElement as HTMLElement).getByText("Passed");

    expect(heading).toHaveTextContent(/^nightly, /);
    expect(badge.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    expect(factOf("Strategies")).toHaveTextContent("none");
  });

  it("copies the config hash, and links to the comparison with this run as the first and to the list", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const run = benchRunDetail(2);
    renderRun(run);

    await screen.findByRole("heading", { level: 1 });
    await user.click(screen.getByRole("button", { name: "Copy config hash" }));

    expect(writeText).toHaveBeenCalledWith("9f2c1d7e5a6b");
    expect(screen.getByRole("link", { name: "Compare with another run" })).toHaveAttribute("href", `/benchmarks/compare?a=${run.id}`);
    expect(screen.getByRole("link", { name: "All benchmark runs" })).toHaveAttribute("href", "/benchmarks");
  });
});

describe("the page of one benchmark run, scores and thresholds", () => {
  it("shows every metric with its value and its interval, none for a metric without an interval and none yet for one without a value", async () => {
    renderRun(benchRunDetail(2));

    const table = await screen.findByRole("region", { name: "Scores of this run" });
    const labels = within(table)
      .getAllByRole("rowheader")
      .map((h) => h.textContent);

    expect(labels).toEqual([
      "Overall score",
      "Verdict accuracy",
      "False positive precision",
      "False positive recall",
      "True positive precision",
      "True positive recall",
      "Root cause match",
      "Root cause score",
      "Evidence",
      "Proposals correct",
      "Source failures reported",
    ]);

    const cells = (name: string) => within(rowOf(table, new RegExp(`^${name}`))).getAllByRole("cell").map((c) => c.textContent);

    expect(cells("Overall score")).toEqual(["70%", "62% to 88%"]);
    expect(cells("False positive recall")).toEqual(["100%", "60% to 100%"]);
    expect(cells("True positive recall")).toEqual(["50%", "none"]);
    expect(cells("Proposals correct")).toEqual(["none yet", "none"]);
  });

  it("shows each threshold with its limit, its value, a result as a word with an icon, and its detail", async () => {
    renderRun(benchRunDetail(2));

    const table = await screen.findByRole("region", { name: "Thresholds of this run" });
    const cells = (name: string) => within(rowOf(table, new RegExp(`^${name}`))).getAllByRole("cell").map((c) => c.textContent);

    expect(cells("Verdict accuracy")).toEqual(["at least 70%", "75%", "Passed", ""]);
    expect(cells("Root cause match")).toEqual(["at least 70%", "60%", "Failed", ""]);
    expect(cells("Mean cost per run")).toEqual(["at most $0.0010", "$0.0020", "Failed", ""]);
    expect(cells("Proposals correct")).toEqual(["at least 90%", "none yet", "Passed", "nothing to count, so it passes"]);
    expect(within(table).getAllByText("Passed")[0]?.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    expect(within(table).getAllByText("Failed")[0]?.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  });

  it("names the first failed threshold above the tables, and how many others failed", async () => {
    renderRun(benchRunDetail(2));

    const alert = await screen.findByRole("alert");

    expect(within(alert).getByRole("heading", { name: "Failed threshold: Root cause match" })).toBeInTheDocument();
    expect(alert).toHaveTextContent("Value 60%, limit at least 70%.");
    expect(alert).toHaveTextContent("1 other threshold also failed.");
    expect(alert.compareDocumentPosition(screen.getByRole("heading", { name: "Scores", level: 2 })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("names a failed cost threshold with its direction, and does not mention others when it is the only one", async () => {
    renderRun(benchRunDetail(2, { thresholds: [{ name: "maxCostPerRunUSD", limit: 0.001, value: 0.002, passed: false }] }));

    const alert = await screen.findByRole("alert");

    expect(within(alert).getByRole("heading", { name: "Failed threshold: Mean cost per run" })).toBeInTheDocument();
    expect(alert).toHaveTextContent("Value $0.0020, limit at most $0.0010.");
    expect(alert).not.toHaveTextContent("also failed");
  });

  it("shows no alert for a passed run, and says so when the run set no thresholds", async () => {
    renderRun(benchRunDetail(1, { status: "passed", thresholds: [] }));

    await screen.findByRole("heading", { name: "Thresholds", level: 2 });

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByText("This run set no thresholds.")).toBeInTheDocument();
  });

  it("shows the text of a threshold as plain text", async () => {
    renderRun(benchRunDetail(2, { thresholds: [{ name: "<img src=x onerror=alert(1)>", limit: 0.5, passed: true, detail: "<b>bold</b> detail" }] }));

    const table = await screen.findByRole("region", { name: "Thresholds of this run" });

    expect(within(table).getByText("<b>bold</b> detail")).toBeInTheDocument();
    expect(within(table).getByRole("rowheader", { name: "<img src=x onerror=alert(1)>" })).toBeInTheDocument();
    expect(table.querySelector("img, b")).toBeNull();
  });
});

describe("the page of one benchmark run, cost and time", () => {
  it("shows the cost, the judge's cost, the tokens, the tool calls, the means and the judge errors", async () => {
    renderRun(benchRunDetail(2));

    await screen.findByRole("heading", { name: "Cost and time", level: 2 });

    expect(factOf("Total model cost")).toHaveTextContent("$0.0080");
    expect(factOf("Judge cost")).toHaveTextContent("$0.0001");
    expect(factOf("Input tokens")).toHaveTextContent("1,000");
    expect(factOf("Output tokens")).toHaveTextContent("500");
    expect(factOf("Tool calls")).toHaveTextContent("12");
    expect(factOf("Mean cost per run")).toHaveTextContent("$0.0020");
    expect(factOf("Mean time per run")).toHaveTextContent("2 s");
    expect(factOf("Judge errors")).toHaveTextContent("0");
  });

  it("says none yet for the means of a run with nothing to count", async () => {
    const run = benchRunDetail(2);
    renderRun({ ...run, metrics: { ...run.metrics, meanCostPerRunUsd: undefined, meanDurationMs: undefined } });

    await screen.findByRole("heading", { name: "Cost and time", level: 2 });

    expect(factOf("Mean cost per run")).toHaveTextContent("none yet");
    expect(factOf("Mean time per run")).toHaveTextContent("none yet");
  });
});

describe("the page of one benchmark run, scenarios", () => {
  it("lists wrong scenarios first, each with its mark as a word, its spread, its verdicts and its means", async () => {
    renderRun(benchRunDetail(2));

    const table = await screen.findByRole("region", { name: "Scenarios of this run" });
    const cells = (name: string) => within(rowOf(table, new RegExp(name))).getAllByRole("cell").map((c) => c.textContent);

    expect(within(table).getAllByRole("button").map((b) => b.textContent)).toEqual(["oom-after-limit", "disk-flapping", "checkout-pool-size"]);
    expect(cells("oom-after-limit")).toEqual(["Wrong", "Real issue", "0 of 2", "Not sure yet (1), No verdict (1)", "0%", "$0.0020", "750 ms"]);
    expect(cells("disk-flapping")).toEqual(["Partly wrong", "Flapping", "1 of 2", "Flapping (1), Real issue (1)", "0%", "$0.0020", "750 ms"]);
    expect(cells("checkout-pool-size")).toEqual(["Correct", "Real issue", "2 of 2", "Real issue (2)", "75%", "$0.0020", "750 ms"]);
    expect(within(table).getByText("Wrong").querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  });

  it("opens a scenario to the result of each of its repeats, and closes it again", async () => {
    const user = userEvent.setup();
    renderRun(benchRunDetail(2));

    const table = await screen.findByRole("region", { name: "Scenarios of this run" });
    const toggle = within(table).getByRole("button", { name: "oom-after-limit" });

    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(within(table).queryByRole("table", { name: "Repeats of oom-after-limit" })).not.toBeInTheDocument();

    await user.click(toggle);

    const repeats = within(table).getByRole("table", { name: "Repeats of oom-after-limit" });
    const headers = within(repeats)
      .getAllByRole("columnheader")
      .map((h) => h.textContent);
    const cells = (n: number) => within(within(repeats).getAllByRole("row")[n] as HTMLElement).getAllByRole("cell").map((c) => c.textContent);

    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(headers).toEqual(["Repeat", "Verdict given", "Result", "Root cause score", "Required items met", "Evidence score", "Proposals correct", "Cost", "Time", "Tokens", "Tool calls"]);
    expect(within(within(repeats).getAllByRole("row")[1] as HTMLElement).getByRole("rowheader")).toHaveTextContent("1");
    expect(cells(1)).toEqual(["Not sure yet", "Wrong", "0%", "Not met", "25%", "Wrong", "$0.0020", "750 ms", "400 in, 100 out", "3"]);
    expect(cells(2)).toEqual(["No verdict", "Wrong", "Not graded", "Not graded", "Not scored", "Not scored", "$0.0020", "750 ms", "400 in, 100 out", "3"]);

    await user.click(toggle);

    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(within(table).queryByRole("table", { name: "Repeats of oom-after-limit" })).not.toBeInTheDocument();
  });

  it("shows a right repeat as correct and its required items as met", async () => {
    const user = userEvent.setup();
    renderRun(benchRunDetail(2));

    await user.click(await screen.findByRole("button", { name: "checkout-pool-size" }));

    const repeats = screen.getByRole("table", { name: "Repeats of checkout-pool-size" });
    const cells = (n: number) => within(within(repeats).getAllByRole("row")[n] as HTMLElement).getAllByRole("cell").map((c) => c.textContent);

    expect(cells(1).slice(0, 5)).toEqual(["Real issue", "Correct", "100%", "Met", "100%"]);
    expect(cells(2).slice(0, 5)).toEqual(["Real issue", "Correct", "50%", "Not met", "50%"]);
  });

  it("shows scenario IDs and verdicts from the run as plain text", async () => {
    const user = userEvent.setup();
    const hostile = "<img src=x onerror=alert(1)>";
    renderRun(benchRunDetail(2, { results: [benchResult(hostile, 1, { verdict: "<b>odd</b>", verdictCorrect: false, expectedVerdict: "<i>other</i>" })] }));

    await user.click(await screen.findByRole("button", { name: hostile }));

    expect(screen.getAllByText("<b>odd</b>").length).toBeGreaterThan(0);
    expect(screen.getAllByText("<i>other</i>").length).toBeGreaterThan(0);
    expect(screen.getByRole("region", { name: "Scenarios of this run" }).querySelector("img, b, i")).toBeNull();
  });

  it("says when the run has no results", async () => {
    renderRun(benchRunDetail(2, { results: [] }));

    expect(await screen.findByText("No results are stored for this run.")).toBeInTheDocument();
  });
});

describe("the page of one benchmark run, states", () => {
  it("says so, with a link back to the list, when no run has the ID", async () => {
    renderApp({ route: `/benchmarks/${benchRunId(9)}`, handler: () => problem(404, NO_RUN) });

    expect(await screen.findByRole("heading", { name: NO_RUN })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to the benchmark runs" })).toHaveAttribute("href", "/benchmarks");
  });

  it("does not read a word such as latest as a run ID: it is a page that does not exist, and no run is asked for", async () => {
    const { handler, seen } = recording(() => problem(400, "not an ID"));
    renderApp({ route: "/benchmarks/latest", handler });

    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeInTheDocument();
    expect(seen.filter((r) => r.path.startsWith("/api/v1/bench"))).toHaveLength(0);
  });

  it("says why it failed and asks again when Try again is pressed", async () => {
    const user = userEvent.setup();
    const run = benchRunDetail(2);
    let calls = 0;
    const handler = () => (++calls === 1 ? problem(500, "The database is down.") : json(200, run));
    renderApp({ route: `/benchmarks/${run.id}`, handler, queryClient: new QueryClient({ defaultOptions: { queries: { retry: false } } }) });

    expect(await screen.findByText("The database is down.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("heading", { name: nameOf(run), level: 1 })).toBeInTheDocument();
  });

  it("shows Loading while the run is on its way", async () => {
    renderApp({ route: `/benchmarks/${benchRunId(2)}`, handler: () => new Promise<Response>(() => undefined) });

    expect(await screen.findByText("Loading")).toBeInTheDocument();
  });
});
