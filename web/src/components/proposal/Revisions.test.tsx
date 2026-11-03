import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { proposal } from "../../test/actionProposals";
import { renderWithProviders } from "../../test/helpers";
import { ProposalCard } from "../ProposalCard";

const CHANGES = [{ path: "id", before: '"7"', after: '"6"' }];

const REVISIONS = {
  running: { kind: "ReplicaSet revision", id: "7", version: "checkout:1.4.7", deployedAt: "2026-11-03T09:30:00Z", deployedBy: "ana", notes: "app=checkout:1.4.7" },
  goesBackTo: { kind: "ReplicaSet revision", id: "6", version: "checkout:1.4.6" },
};

/** A rollback that waits for a decision. */
function rollback(over: Parameters<typeof proposal>[0] = {}) {
  return proposal({ action: "checkout-rollback", target: "kubernetes:deployment/shop/checkout", changes: CHANGES, revisions: REVISIONS, ...over });
}

describe("the revisions section", () => {
  it("shows what runs now and what it goes back to, with the deploy time and who deployed", () => {
    renderWithProviders(<ProposalCard proposal={rollback()} canDecide />);

    const section = screen.getByRole("region", { name: "Revisions" });
    expect(within(section).getByText("Rolls back from checkout:1.4.7 to checkout:1.4.6.")).toBeInTheDocument();

    const now = within(section).getByRole("region", { name: "Running now, ReplicaSet revision" });
    expect(now).toHaveTextContent("Revision: ReplicaSet revision 7");
    expect(now).toHaveTextContent("Version: checkout:1.4.7");
    expect(now).toHaveTextContent("Deployed by: ana");
    expect(now).toHaveTextContent("Notes: app=checkout:1.4.7");
    expect(now).not.toHaveTextContent("Deployed at: unknown");

    const back = within(section).getByRole("region", { name: "Goes back to, ReplicaSet revision" });
    expect(back).toHaveTextContent("Revision: ReplicaSet revision 6");
    expect(back).toHaveTextContent("Deployed at: unknown");
    expect(back).toHaveTextContent("Deployed by: unknown");
    expect(back).not.toHaveTextContent("Notes:");
  });

  it("names a revision by its kind and ID when it has no version", () => {
    const bare = { running: { kind: "Helm release revision", id: "12" }, goesBackTo: { kind: "Helm release revision", id: "11" } };
    renderWithProviders(<ProposalCard proposal={rollback({ revisions: bare })} canDecide />);

    expect(screen.getByText("Rolls back from Helm release revision 12 to Helm release revision 11.")).toBeInTheDocument();
    expect(screen.getAllByText(/Version: unknown/)).toHaveLength(2);
  });

  it("shows hidden characters as the \\u escapes the server wrote, and text from outside as text", () => {
    const hostile = {
      running: { ...REVISIONS.running, version: "v1\\u202e2", deployedBy: "<img src=x onerror=alert(1)>" },
      goesBackTo: REVISIONS.goesBackTo,
    };
    const { container } = renderWithProviders(<ProposalCard proposal={rollback({ revisions: hostile })} canDecide />);

    expect(screen.getAllByText(/v1\\u202e2/).length).toBeGreaterThan(0);
    expect(screen.getByText(/<img src=x onerror=alert\(1\)>/)).toBeInTheDocument();
    expect(container.querySelector("img")).toBeNull();
  });

  it("replaces the field-by-field list instead of adding to it", () => {
    renderWithProviders(<ProposalCard proposal={rollback()} canDecide />);

    expect(screen.queryByRole("region", { name: "Changes, field by field" })).not.toBeInTheDocument();
  });

  it("shows nothing for a proposal that is not a rollback", () => {
    renderWithProviders(<ProposalCard proposal={proposal({ target: "datadog:monitor/123", changes: CHANGES })} canDecide />);

    expect(screen.queryByRole("region", { name: "Revisions" })).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Changes, field by field" })).toBeInTheDocument();
  });

  it("shows the revisions of an undo the other way round", () => {
    const swapped = { running: REVISIONS.goesBackTo, goesBackTo: REVISIONS.running };
    renderWithProviders(<ProposalCard proposal={rollback({ status: "succeeded", undoOf: "0192d3a4-0000-7000-8000-0000000000f2", revisions: swapped })} canDecide />);

    expect(screen.getByText("Rolls back from checkout:1.4.6 to checkout:1.4.7.")).toBeInTheDocument();
  });

  it("is listed between the changes and the reason", () => {
    renderWithProviders(<ProposalCard proposal={rollback()} canDecide />);

    const revisions = screen.getByRole("region", { name: "Revisions" });
    const reason = screen.getByText("Reason, written by the model and not checked");
    expect(Boolean(revisions.compareDocumentPosition(reason) & Node.DOCUMENT_POSITION_FOLLOWING)).toBe(true);
  });

  it("repeats the two versions when Approve asks again", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ProposalCard proposal={rollback()} canDecide />);

    await user.click(screen.getByRole("button", { name: "Approve checkout-rollback" }));

    expect(screen.getByText("Roll back checkout-rollback from checkout:1.4.7 to checkout:1.4.6, as shown above?")).toBeInTheDocument();
  });
});
