import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderWithProviders } from "../../test/helpers";
import { benchRun, benchRunId } from "../../test/bench";
import { nameOf, RunsTable } from "./RunsTable";

function rowOf(name: RegExp): HTMLElement {
  return screen.getByRole("row", { name });
}

describe("RunsTable", () => {
  it("shows a row for each run, with its result as a word and an icon, and the name as a link to the run", () => {
    renderWithProviders(<RunsTable label="Benchmark runs" runs={[benchRun(1, { label: "baseline" }), benchRun(2)]} />);

    const table = screen.getByRole("region", { name: "Benchmark runs" });
    const passed = within(table).getByText("Passed");
    const failed = within(table).getByText("Failed");

    expect(passed.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    expect(failed.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    expect(within(table).getByRole("link", { name: /^baseline, / })).toHaveAttribute("href", `/benchmarks/${benchRunId(1)}`);
    expect(within(table).getByRole("link", { name: /^Run of / })).toHaveAttribute("href", `/benchmarks/${benchRunId(2)}`);
  });

  it("shows the version and models under the name, the scenario set with its repeats, and who started the run, in the raw value when it is not a known one", () => {
    renderWithProviders(
      <RunsTable
        label="Benchmark runs"
        runs={[
          benchRun(1, { repeats: 1, triggeredBy: "ci", metrics: { ...benchRun(1).metrics, runs: 1 } }),
          benchRun(2, { repeats: 3, triggeredBy: "gate", metrics: { ...benchRun(2).metrics, runs: 6 } }),
          benchRun(3, { triggeredBy: "scheduler" }),
        ]}
      />,
    );

    const [, one, two, three] = screen.getAllByRole("row");

    expect(within(one as HTMLElement).getByText("v0.9.1, big-model")).toBeInTheDocument();
    expect(within(one as HTMLElement).getByText("1 repeat, 1 run")).toBeInTheDocument();
    expect(within(one as HTMLElement).getByText("CI")).toBeInTheDocument();
    expect(within(two as HTMLElement).getByText("3 repeats, 6 runs")).toBeInTheDocument();
    expect(within(two as HTMLElement).getByText("Strategy gate")).toBeInTheDocument();
    expect(within(three as HTMLElement).getByText("scheduler")).toBeInTheDocument();
  });

  it("says none yet for a score the run has no value for", () => {
    const run = benchRun(1);
    renderWithProviders(<RunsTable label="Benchmark runs" runs={[{ ...run, metrics: { ...run.metrics, meanCostPerRunUsd: undefined, meanDurationMs: undefined } }]} />);

    expect(within(rowOf(/Run of /)).getAllByText("none yet")).toHaveLength(2);
  });
});

describe("nameOf", () => {
  it("uses the label when there is one and the start when there is not", () => {
    expect(nameOf(benchRun(1, { label: "nightly" }))).toMatch(/^nightly, /);
    expect(nameOf(benchRun(1))).toMatch(/^Run of /);
  });
});
