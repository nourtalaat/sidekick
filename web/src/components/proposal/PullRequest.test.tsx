import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { formatTime } from "../../format";
import { ci, proposal, pullRequest } from "../../test/actionProposals";
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
    { state: "open", word: "Open", tone: "info", icon: "lucide-git-pull-request", over: {}, moments: [] },
    { state: "merged", word: "Merged", tone: "success", icon: "lucide-git-merge", over: { mergedAt: MERGED_AT }, moments: [`Merged ${formatTime(MERGED_AT)}`] },
    { state: "closed", word: "Closed", tone: "neutral", icon: "lucide-git-pull-request-closed", over: { closedAt: CLOSED_AT }, moments: [`Closed ${formatTime(CLOSED_AT)}`] },
  ] as const)("shows a $state pull request with its word, an icon and when it was checked", ({ state, word, tone, icon, over, moments }) => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest({ state, number: 12, ...over }) })} canDecide />);

    const region = section() as HTMLElement;
    const badge = within(region).getByText(word, { selector: ".sk-badge" });

    expect(badge).toHaveClass(`sk-badge--status-${tone}`);
    expect(badge.querySelector("svg")).toHaveClass(icon);
    expect(badge.querySelector(".lucide-clock")).toBeNull();
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

const CI_AT = "2026-11-02T06:10:00Z";

/** The CI line of the card, or null when the card has none. */
function ciLine() {
  return within(section() as HTMLElement)
    .getAllByRole("listitem")
    .find((item) => item.querySelector(".sk-proposal__ci") !== null);
}

describe("the CI line", () => {
  it.each([
    { name: "passed", over: { state: "passed", passed: 7, total: 7 }, word: "Passed", tone: "success", icon: /lucide-check/, text: "7 of 7 checks passed" },
    { name: "one check that passed", over: { state: "passed", passed: 1, total: 1 }, word: "Passed", tone: "success", icon: /lucide-check/, text: "1 of 1 check passed" },
    { name: "failed", over: { state: "failed", passed: 5, total: 7, failedCount: 2 }, word: "Failed", tone: "danger", icon: /lucide-circle-x/, text: "2 of 7 checks failed" },
    { name: "none", over: { state: "none", passed: 0, total: 0 }, word: "None", tone: "neutral", icon: /lucide-circle-slash/, text: "No check or pipeline started" },
    {
      name: "unknown",
      over: { state: "unknown", passed: 0, total: 0, message: "Sidekick could not read the checks. The GitHub App needs Checks (read)." },
      word: "Unknown",
      tone: "warning",
      icon: /lucide-circle-(question-mark|help)/,
      text: "Sidekick could not read the checks. The GitHub App needs Checks (read).",
    },
    { name: "timed out", over: { state: "timed_out", passed: 5, total: 7 }, word: "Not finished", tone: "warning", icon: /lucide-hourglass/, text: "Sidekick stopped waiting; 5 of 7 checks had passed" },
    { name: "pending", over: { state: "pending", passed: 1, total: 3, reportedAt: undefined }, word: "Running", tone: "info", icon: /lucide-loader/, text: "1 of 3 checks passed so far" },
    { name: "pending before the first read", over: { state: "pending", passed: 0, total: 0, checkedAt: undefined, reportedAt: undefined }, word: "Running", tone: "info", icon: /lucide-loader/, text: "Waiting for the checks" },
  ] as const)("shows $name with its word, an icon and what Sidekick found", ({ over, word, tone, icon, text }) => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest({ ci: ci(over) }) })} canDecide />);

    const line = within(ciLine() as HTMLElement);
    const badge = line.getByText(word, { selector: ".sk-badge" });

    expect(badge).toHaveClass(`sk-badge--status-${tone}`);
    expect(badge.querySelector("svg")?.getAttribute("class")).toMatch(icon);
    expect(line.getByText(text)).toBeInTheDocument();
    expect(line.getByText("CI")).toBeInTheDocument();
  });

  it("says when Sidekick reported, or last read while it waits", () => {
    const { unmount } = renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest({ ci: ci() }) })} canDecide />);

    expect(ciLine()?.textContent).toContain(`Reported ${formatTime(CI_AT)}`);

    unmount();

    renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest({ ci: ci({ state: "pending", reportedAt: undefined }) }) })} canDecide />);

    expect(ciLine()?.textContent).toContain(`Checked ${formatTime(CI_AT)}`);
  });

  it("names the failed checks, linking those with an http or https address, and says how many more there are", () => {
    renderWithProviders(
      <ProposalCard
        proposal={proposal({
          status: "succeeded",
          pullRequest: pullRequest({
            ci: ci({
              state: "failed",
              passed: 2,
              total: 6,
              failedCount: 4,
              failed: [
                { name: "unit tests", url: "https://ci.example.com/1" },
                { name: "lint", url: "javascript:alert(1)" },
                { name: "e2e" },
              ],
            }),
          }),
        })}
        canDecide
      />,
    );

    const failed = within(within(ciLine() as HTMLElement).getByRole("list", { name: "Failed checks" }));

    expect(failed.getAllByRole("listitem").map((item) => item.textContent)).toEqual(["unit tests", "lint", "e2e", "and 1 more"]);
    expect(failed.getByRole("link", { name: "unit tests" })).toHaveAttribute("href", "https://ci.example.com/1");
    expect(failed.getAllByRole("link")).toHaveLength(1);
  });

  it("shows a check name with markup or hidden characters as the text it is", () => {
    const name = "<img src=x onerror=alert(1)> <b>tests</b>\u202e\u200b";

    const { container } = renderWithProviders(
      <ProposalCard
        proposal={proposal({
          status: "succeeded",
          pullRequest: pullRequest({ ci: ci({ state: "failed", passed: 0, total: 1, failedCount: 1, failed: [{ name, url: "https://ci.example.com/1" }] }) }),
        })}
        canDecide
      />,
    );

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
    expect(within(ciLine() as HTMLElement).getByRole("link").textContent).toBe(name);
  });

  it("is a line of its own, after the state line, and absent when Sidekick does not follow the CI", () => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest({ ci: ci() }) })} canDecide />);

    const items = within(section() as HTMLElement).getAllByRole("listitem");

    expect(items).toHaveLength(2);
    expect(items[0]?.querySelector(".sk-proposal__ci")).toBeNull();
    expect(items[1]?.querySelector(".sk-proposal__ci")).not.toBeNull();
  });

  it("shows no CI line for a pull request without one", () => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", pullRequest: pullRequest() })} canDecide />);

    expect(ciLine()).toBeUndefined();
  });
});
