import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusBadge, VerdictBadge, type Status, type Verdict } from "./Badge";

const VERDICT_LABELS: [Verdict, string][] = [
  ["true_positive", "Real issue"],
  ["false_positive", "False alarm"],
  ["inconclusive", "Not sure yet"],
  ["duplicate", "Duplicate"],
  ["known_issue", "Known issue"],
  ["flapping", "Flapping"],
];

const STATUS_LABELS: [Status, string][] = [
  ["success", "Done"],
  ["info", "Updated"],
  ["warning", "Source unavailable"],
  ["danger", "Failed"],
  ["neutral", "Queued"],
];

describe("VerdictBadge", () => {
  it.each(VERDICT_LABELS)("shows %s as the text %s next to a hidden icon", (verdict, label) => {
    const { container } = render(<VerdictBadge verdict={verdict} />);

    expect(screen.getByText(label)).toBeInTheDocument();
    expect(container.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  });

  it("has a label for every verdict a report can have", () => {
    const { container } = render(
      <>
        {VERDICT_LABELS.map(([verdict]) => (
          <VerdictBadge key={verdict} verdict={verdict} />
        ))}
      </>,
    );

    expect(container.textContent).toBe(VERDICT_LABELS.map(([, label]) => label).join(""));
  });
});

describe("StatusBadge", () => {
  it.each(STATUS_LABELS)("shows the %s status with its label %s and a hidden icon", (status, label) => {
    const { container } = render(<StatusBadge status={status}>{label}</StatusBadge>);

    expect(screen.getByText(label)).toBeInTheDocument();
    expect(container.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  });

  it("spins the icon of a running status, and keeps the word", () => {
    const { container } = render(
      <StatusBadge status="info" spinning>
        Running
      </StatusBadge>,
    );

    expect(container.querySelector("svg")).toHaveClass("sk-spin");
    expect(screen.getByText("Running")).toBeInTheDocument();
  });
});
