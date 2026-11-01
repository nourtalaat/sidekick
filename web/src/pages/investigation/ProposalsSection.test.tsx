import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderWithProviders } from "../../test/helpers";
import { proposal } from "../../test/actionProposals";
import { ProposalsSection } from "./ProposalsSection";

describe("ProposalsSection", () => {
  it("shows nothing when the model proposed nothing", () => {
    const { container } = renderWithProviders(<ProposalsSection proposals={[]} canDecide />);

    expect(container).toBeEmptyDOMElement();
  });

  it("lists the proposals under a heading, with the buttons for an approver", () => {
    renderWithProviders(<ProposalsSection proposals={[proposal(), proposal({ id: "0192d3a4-0000-7000-8000-0000000000f2", action: "slack-react", status: "rejected" })]} canDecide />);

    expect(screen.getByRole("heading", { name: "Proposed actions", level: 2 })).toBeInTheDocument();
    expect(screen.getAllByRole("article")).toHaveLength(2);
    expect(screen.getAllByRole("button")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Approve monitor-note" })).toBeInTheDocument();
  });

  it("gives everyone else the proposals without buttons", () => {
    renderWithProviders(<ProposalsSection proposals={[proposal()]} canDecide={false} />);

    expect(screen.getByText("Parameters, exactly as they will run")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
