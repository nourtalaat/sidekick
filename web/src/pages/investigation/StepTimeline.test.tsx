import { render, screen } from "@testing-library/react";
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
