import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { proposal } from "../../test/actionProposals";
import { renderWithProviders } from "../../test/helpers";
import { ProposalCard } from "../ProposalCard";
import type { Section } from "./section";
import { sections } from "./sections";

const original = [...sections];

afterEach(() => {
  sections.splice(0, sections.length, ...original);
});

/** Whether a comes before b in the document. */
function before(a: HTMLElement, b: HTMLElement): boolean {
  return Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);
}

describe("the list of sections", () => {
  it("shows the sections in the order of the list", () => {
    renderWithProviders(<ProposalCard proposal={proposal({ changes: [{ path: "tags", before: "1", after: "2" }], status: "failed", error: "Down." })} canDecide />);

    const route = screen.getByText("Route");
    const params = screen.getByText("Parameters, exactly as they will run");
    const changes = screen.getByRole("region", { name: "Changes, field by field" });
    const reason = screen.getByText("Reason, written by the model and not checked");
    const outcome = screen.getByText("Why it failed");

    expect(before(route, params)).toBe(true);
    expect(before(params, changes)).toBe(true);
    expect(before(changes, reason)).toBe(true);
    expect(before(reason, outcome)).toBe(true);
  });

  it("shows a section that is added to the list between the others, and edits nothing else", () => {
    const Fake: Section = () => <p>A section from a later ticket</p>;
    const AFTER_CHANGES = 3;
    sections.splice(AFTER_CHANGES, 0, Fake);

    renderWithProviders(<ProposalCard proposal={proposal({ changes: [{ path: "tags", before: "1", after: "2" }] })} canDecide />);

    const changes = screen.getByRole("region", { name: "Changes, field by field" });
    const fake = screen.getByText("A section from a later ticket");
    const reason = screen.getByText("Reason, written by the model and not checked");

    expect(before(changes, fake)).toBe(true);
    expect(before(fake, reason)).toBe(true);
  });

  it("leaves out a section that has nothing to show", () => {
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide />);

    expect(screen.queryByRole("region", { name: "Changes, field by field" })).not.toBeInTheDocument();
    expect(screen.queryByText("Result")).not.toBeInTheDocument();
  });
});
