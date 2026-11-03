import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { formatTime } from "../../format";
import { proposal, pullRequest } from "../../test/actionProposals";
import { renderWithProviders } from "../../test/helpers";
import { ProposalCard } from "../ProposalCard";

const MERGED_AT = "2026-11-01T15:30:00Z";
const CLOSED_AT = "2026-11-01T09:00:00Z";
const CHECKED_AT = "2026-11-02T06:00:00Z";

/** The section of the card, or null when the card has none. */
function section() {
  return screen.queryByRole("region", { name: "Pull request" });
}

describe("the pull request section", () => {
  it.each([
    { state: "open", word: "Open", tone: "info", over: {}, moments: [] },
    { state: "merged", word: "Merged", tone: "success", over: { mergedAt: MERGED_AT }, moments: [`Merged ${formatTime(MERGED_AT)}`] },
    { state: "closed", word: "Closed", tone: "neutral", over: { closedAt: CLOSED_AT }, moments: [`Closed ${formatTime(CLOSED_AT)}`] },
  ] as const)("shows a $state pull request with its word, an icon and when it was checked", ({ state, word, tone, over, moments }) => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest({ state, number: 12, ...over }) })} canDecide />);

    const region = section() as HTMLElement;
    const badge = within(region).getByText(word, { selector: ".sk-badge" });

    expect(badge).toHaveClass(`sk-badge--status-${tone}`);
    expect(badge.querySelector("svg")).not.toBeNull();
    expect(within(region).getByRole("link", { name: "acme/monitors #12" })).toHaveAttribute("href", "https://github.com/acme/monitors/pull/12");

    const times = Array.from(region.querySelectorAll(".sk-proposal__pr-moment"), (el) => el.textContent);

    expect(times).toEqual([...moments, `Checked ${formatTime(CHECKED_AT)}`]);
  });

  it("renders with the state alone, as one line of the list", () => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest() })} canDecide />);

    expect(within(section() as HTMLElement).getAllByRole("listitem")).toHaveLength(1);
  });

  it("shows nothing for a proposal without a pull request", () => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded" })} canDecide />);

    expect(section()).not.toBeInTheDocument();
    expect(screen.queryByText("Pull request")).not.toBeInTheDocument();
  });

  it("shows an address that is not http or https as text, never as a link", () => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest({ url: "javascript:alert(1)" }) })} canDecide />);

    const region = within(section() as HTMLElement);

    expect(region.queryByRole("link")).not.toBeInTheDocument();
    expect(region.getByText("acme/monitors #12")).toBeInTheDocument();
  });

  it("shows the repository as text, never as markup", () => {
    const { container } = renderWithProviders(
      <ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest({ repo: "<img src=x onerror=alert(1)>/<b>x</b>" }) })} canDecide />,
    );

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
    expect(screen.getByRole("link", { name: "<img src=x onerror=alert(1)>/<b>x</b> #12" })).toBeInTheDocument();
  });

  it("comes after the outcome", () => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", result: { summary: "Opened a pull request." }, pullRequest: pullRequest() })} canDecide />);

    const outcome = screen.getByText("Opened a pull request.");

    expect(Boolean(outcome.compareDocumentPosition(section() as HTMLElement) & Node.DOCUMENT_POSITION_FOLLOWING)).toBe(true);
  });
});
