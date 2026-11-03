import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BenchComparison } from "../../api/bench";
import { renderWithProviders } from "../../test/helpers";
import { benchComparison } from "../../test/stats";
import { ComparisonTables } from "./ComparisonTables";

function renderTables(comparison: BenchComparison = benchComparison()) {
  renderWithProviders(<ComparisonTables comparison={comparison} />);

  return {
    metrics: screen.getByRole("region", { name: /^Metrics of / }),
    scenarios: screen.getByRole("region", { name: /^Scenarios of / }),
  };
}

describe("ComparisonTables, metrics", () => {
  it("shows the values, the change with its sign and unit, and the difference in words", () => {
    const { metrics } = renderTables();

    expect(within(metrics).getByRole("row", { name: /^overall score/ })).toHaveTextContent("70%40%-30 pointsClear difference");
    expect(within(metrics).getByRole("row", { name: /^verdict accuracy/ })).toHaveTextContent("No clear difference");
    expect(within(metrics).getByRole("row", { name: /^true positive recall/ })).toHaveTextContent("50%none yetnoneNot compared");
    expect(within(metrics).getByRole("row", { name: /^mean cost per run/ })).toHaveTextContent("+$0.0020");
    expect(within(metrics).getByRole("row", { name: /^mean time per run/ })).toHaveTextContent("+1 s");
    expect(within(metrics).getByText(/A difference is clear when the confidence intervals of the two runs do not overlap\./)).toBeInTheDocument();
  });
});

describe("ComparisonTables, scenarios", () => {
  it("lists the scenario that went from right to wrong first, with its words and an icon", () => {
    const { scenarios } = renderTables();

    const [, first] = within(scenarios).getAllByRole("row");

    expect(first).toHaveTextContent("s1");
    expect(within(first as HTMLElement).getByText("Right to wrong").querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  });

  it("marks a scenario that went from wrong to right, and puts it after the one that went from right to wrong", () => {
    const comparison = benchComparison();
    comparison.scenarios = [
      { scenarioId: "a-better", expectedVerdict: "true_positive", a: { runs: 2, correct: 0 }, b: { runs: 2, correct: 2, rootCauseMean: 1 } },
      ...comparison.scenarios,
    ];
    const { scenarios } = renderTables(comparison);

    const rows = within(scenarios).getAllByRole("row");

    expect(rows[1]).toHaveTextContent("s1");
    expect(rows[2]).toHaveTextContent("a-better");
    expect(within(rows[2] as HTMLElement).getByText("Wrong to right")).toBeInTheDocument();
  });

  it("shows the correct runs and the mean root cause score of each run, and says when a run did not have the scenario or did not grade it", () => {
    const { scenarios } = renderTables();

    expect(within(scenarios).getByRole("row", { name: /^s1/ })).toHaveTextContent("False alarmRight to wrong2 of 285%0 of 1not graded");
    expect(within(scenarios).getByRole("row", { name: /^s2/ })).toHaveTextContent("Only in Run A1 of 2not gradednot runnot run");
    expect(within(scenarios).getByRole("row", { name: /^s3/ })).toHaveTextContent("Only in Run Bnot runnot run1 of 1not graded");
  });

  it("says Same result and Changed in part for the rest", () => {
    const comparison = benchComparison();
    comparison.scenarios = [
      { scenarioId: "same", expectedVerdict: "true_positive", a: { runs: 1, correct: 1 }, b: { runs: 1, correct: 1 } },
      { scenarioId: "partly", expectedVerdict: "true_positive", a: { runs: 2, correct: 2 }, b: { runs: 2, correct: 1 } },
    ];
    const { scenarios } = renderTables(comparison);

    expect(within(scenarios).getByRole("row", { name: /^same/ })).toHaveTextContent("Same result");
    expect(within(scenarios).getByRole("row", { name: /^partly/ })).toHaveTextContent("Changed in part");
  });
});
