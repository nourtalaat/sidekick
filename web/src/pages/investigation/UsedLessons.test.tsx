import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { StepLessons as StepLessonsRecord, UsedLesson } from "../../api/lessons";
import { StepLessons } from "./StepLessons";
import { UsedLessons } from "./UsedLessons";

function used(over: Partial<UsedLesson> = {}): UsedLesson {
  return { id: "l1", text: "Check the deploys first.", scope: { service: "checkout-api" }, status: "active", ...over };
}

describe("the lessons used card", () => {
  it("lists the lessons with where they apply, as plain text", () => {
    render(
      <UsedLessons
        lessons={[used({ text: "<i>Check</i> the deploys." }), used({ id: "l2", text: "Say what you could not check.", scope: {} })]}
      />,
    );

    const card = screen.getByRole("region", { name: "Lessons used" });
    const items = within(card).getAllByRole("listitem");

    expect(items).toHaveLength(2);
    expect(within(items[0] as HTMLElement).getByText("<i>Check</i> the deploys.")).toBeInTheDocument();
    expect(card.querySelector("i")).toBeNull();
    expect(within(items[0] as HTMLElement).getByText(/Applies to service checkout-api/)).toBeInTheDocument();
    expect(within(items[1] as HTMLElement).getByText(/Applies to every investigation/)).toBeInTheDocument();
  });

  it("says what became of a lesson since", () => {
    render(<UsedLessons lessons={[used({ status: "disabled" }), used({ id: "l2", status: "rejected" }), used({ id: "l3", status: "proposed" })]} />);

    expect(screen.getByText("(disabled since)")).toBeInTheDocument();
    expect(screen.getByText("(rejected since)")).toBeInTheDocument();
    expect(screen.getByText("(waits for review)")).toBeInTheDocument();
  });

  it.each([[undefined], [[]]])("says when no lesson was shown (%j)", (lessons) => {
    render(<UsedLessons lessons={lessons} />);

    expect(screen.getByText("No lesson was shown to the model.")).toBeInTheDocument();
  });
});

describe("the lessons of a prompt step", () => {
  it("says how many lessons were used and which did not fit, with why", () => {
    const lessons: StepLessonsRecord = {
      used: ["l1", "l2"],
      leftOut: [
        { id: "l3", reason: "max_tokens", scope: { service: "db" } },
        { id: "l4", reason: "global_share", scope: {} },
        { id: "l5", reason: "max_per_prompt", scope: { route: "payments", strategy: "latency-alert" } },
      ],
      leftOutTotal: 3,
      candidates: 5,
    };

    render(<StepLessons lessons={lessons} />);

    expect(screen.getByText("2 lessons used, from 5 that applied.")).toBeInTheDocument();
    expect(screen.getByText(/3 left out\./)).toBeInTheDocument();
    expect(screen.getByText("Lesson for service db: it did not fit in the tokens set aside for lessons")).toBeInTheDocument();
    expect(screen.getByText("Lesson for every investigation: global lessons had their share of the prompt already")).toBeInTheDocument();
    expect(screen.getByText("Lesson for route payments, strategy latency-alert: the prompt already had the most lessons")).toBeInTheDocument();
  });

  it("says when only some of the lessons left out are listed", () => {
    render(<StepLessons lessons={{ used: ["l1"], leftOut: [{ id: "l3", reason: "max_tokens", scope: {} }], leftOutTotal: 70, candidates: 71 }} />);

    expect(screen.getByText("1 lesson used, from 71 that applied.")).toBeInTheDocument();
    expect(screen.getByText(/70 left out; the first 1 are listed\./)).toBeInTheDocument();
  });

  it("is short when everything fit", () => {
    render(<StepLessons lessons={{ used: [], leftOut: [], leftOutTotal: 0, candidates: 0 }} />);

    expect(screen.getByText("0 lessons used, from 0 that applied.")).toBeInTheDocument();
    expect(screen.queryByText(/left out/)).not.toBeInTheDocument();
  });
});
