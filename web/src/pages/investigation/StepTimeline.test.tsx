import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Step } from "../../api/investigations";
import { step } from "../../test/fixtures";
import { StepTimeline } from "./StepTimeline";

function renderSteps(steps: Step[]) {
  render(<StepTimeline steps={steps} live={false} hasMore={false} loadingMore={false} onLoadMore={() => undefined} />);
}

describe("the meta line of a step", () => {
  it.each([
    {
      name: "a model turn shows time, tokens, cost and model",
      step: step(1, { costMicros: 12_300, model: "big-model" }),
      want: "1 s · 100 in, 20 out · $0.0123 · big-model",
    },
    {
      name: "a tool result shows only the time",
      step: step(1, { kind: "tool_result", tool: "search_logs", durationMs: 300, tokens: undefined }),
      want: "300 ms",
    },
    {
      name: "a cost of zero is left out and the model stays",
      step: step(1, { costMicros: 0, model: "free-model" }),
      want: "1 s · 100 in, 20 out · free-model",
    },
    {
      name: "a cost of zero with no model shows time and tokens",
      step: step(1, { costMicros: 0 }),
      want: "1 s · 100 in, 20 out",
    },
  ])("$name", ({ step: given, want }) => {
    renderSteps([given]);

    expect(screen.getByText(want)).toHaveClass("sk-step__meta");
  });
});

describe("the model line of an opened step", () => {
  it.each([
    { name: "the model and the connector", over: { model: "big-model", connector: "anthropic" }, want: "Model: big-model through anthropic" },
    { name: "only the model", over: { model: "big-model" }, want: "Model: big-model" },
    { name: "only the connector", over: { connector: "anthropic" }, want: "Connector: anthropic" },
  ])("shows $name", async ({ over, want }) => {
    const user = userEvent.setup();
    renderSteps([step(1, over)]);

    expect(screen.queryByText(want)).not.toBeInTheDocument();

    await user.click(screen.getByRole("button"));

    expect(screen.getByText(want)).toBeInTheDocument();
  });

  it("shows no line when the step has neither, and shows the names as plain text", async () => {
    const user = userEvent.setup();
    renderSteps([step(1), step(2, { model: "<b>m</b>", connector: "<i>c</i>" })]);

    const [plain, hostile] = screen.getAllByRole("button");
    await user.click(plain as HTMLElement);
    await user.click(hostile as HTMLElement);

    expect(screen.getByText("Model: <b>m</b> through <i>c</i>")).toBeInTheDocument();
    expect(document.querySelector(".sk-step__model b, .sk-step__model i")).toBeNull();
    expect(document.querySelectorAll(".sk-step__model")).toHaveLength(1);
  });
});
