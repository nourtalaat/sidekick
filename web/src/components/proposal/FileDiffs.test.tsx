import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { ProposalDiffLine } from "../../api/proposals";
import { APPROVER, PROPOSAL_ID, fileDiff, proposal, proposalFile } from "../../test/actionProposals";
import { fakeServer, json, problem, renderWithProviders } from "../../test/helpers";
import { routes } from "../../test/streams";
import { ProposalCard } from "../ProposalCard";

const PATH = "prometheus/rules/checkout.yaml";
const CUT_AT = 400;

/** A fake server that answers the diff of any file with answer, and records what was asked. */
function diffServer(answer: (path: string, full: boolean) => Response = (path) => json(200, fileDiff({ path }))) {
  const asked: { path: string; full: boolean }[] = [];

  const { api } = fakeServer(
    APPROVER,
    routes({
      [`GET /api/v1/proposals/${PROPOSAL_ID}/files/diff`]: (request) => {
        const url = new URL(request.url);
        const path = url.searchParams.get("path") ?? "";
        const full = url.searchParams.get("full") === "true";
        asked.push({ path, full });

        return answer(path, full);
      },
    }),
  );

  return { api, asked };
}

/** The proposal card for a proposal with files. */
function card(paths: string[], over: Parameters<typeof proposalFile>[0] = {}) {
  return <ProposalCard proposal={proposal({ files: paths.map((path) => proposalFile({ path, ...over })) })} canDecide />;
}

function paths(count: number): string[] {
  return Array.from({ length: count }, (_, i) => `rules/file-${i + 1}.yaml`);
}

function disclosures(container: HTMLElement): HTMLDetailsElement[] {
  return Array.from(container.querySelectorAll("details"));
}

function manyLines(count: number): ProposalDiffLine[] {
  return Array.from({ length: count }, (_, i) => ({ kind: "added", newLine: i + 1, text: `line ${i + 1}` }));
}

describe("the files of a pull request proposal", () => {
  it("shows no section for a proposal without files", () => {
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide />);

    expect(screen.queryByRole("region", { name: "Files" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Files" })).not.toBeInTheDocument();
  });

  it("shows one file open, with its path, what happens to it and its counts, and its diff", async () => {
    const { api, asked } = diffServer();
    const { container } = renderWithProviders(card([PATH]), { api });

    expect(screen.getByRole("heading", { name: "Files" })).toBeInTheDocument();
    expect(screen.getByText(PATH)).toBeInTheDocument();
    expect(screen.getByText("Changed")).toBeInTheDocument();
    expect(screen.getByText("+1 -1")).toBeInTheDocument();
    expect(screen.getByText("1 line added, 1 line removed")).toBeInTheDocument();
    expect(disclosures(container).map((d) => d.open)).toEqual([true]);

    const diff = await screen.findByRole("region", { name: `Changes in ${PATH}` });
    expect(diff).toHaveAttribute("tabindex", "0");
    expect(asked).toEqual([{ path: PATH, full: false }]);

    const removed = within(diff).getByText("for: 1m").closest("tr") as HTMLElement;
    expect(removed).toHaveClass("sk-file__line--removed");
    expect(within(removed).getByText("Removed:")).toBeInTheDocument();
    expect(within(removed).getAllByRole("cell").slice(0, 2).map((c) => c.textContent)).toEqual(["5", ""]);
    expect(within(removed).getByText("-")).toBeInTheDocument();

    const added = within(diff).getByText("for: 10m").closest("tr") as HTMLElement;
    expect(added).toHaveClass("sk-file__line--added");
    expect(within(added).getByText("Added:")).toBeInTheDocument();
    expect(within(added).getAllByRole("cell").slice(0, 2).map((c) => c.textContent)).toEqual(["", "5"]);
    expect(within(added).getByText("+")).toBeInTheDocument();

    const context = within(diff).getByText("- alert: Slow").closest("tr") as HTMLElement;
    expect(within(context).getAllByRole("cell").slice(0, 2).map((c) => c.textContent)).toEqual(["4", "4"]);
  });

  it("opens each of two files and reads each", async () => {
    const { api, asked } = diffServer();
    const { container } = renderWithProviders(card(paths(2)), { api });

    expect(disclosures(container).map((d) => d.open)).toEqual([true, true]);
    expect(await screen.findAllByRole("region", { name: /^Changes in / })).toHaveLength(2);
    expect(asked.map((a) => a.path).sort()).toEqual(paths(2));
  });

  it("opens three files, and closes five, reading nothing until one is opened", async () => {
    const user = userEvent.setup();
    const three = diffServer();
    const first = renderWithProviders(card(paths(3)), { api: three.api });

    expect(disclosures(first.container).map((d) => d.open)).toEqual([true, true, true]);
    await screen.findAllByRole("region", { name: /^Changes in / });
    first.unmount();

    const five = diffServer();
    const { container } = renderWithProviders(card(paths(5)), { api: five.api });

    expect(disclosures(container).map((d) => d.open)).toEqual([false, false, false, false, false]);
    expect(five.asked).toEqual([]);

    await user.click(screen.getByText("rules/file-2.yaml"));

    expect(await screen.findByRole("region", { name: "Changes in rules/file-2.yaml" })).toBeInTheDocument();
    expect(five.asked).toEqual([{ path: "rules/file-2.yaml", full: false }]);
    expect(disclosures(container).map((d) => d.open)).toEqual([false, true, false, false, false]);
  });

  it("marks where lines were left out between two changes", async () => {
    const lines: ProposalDiffLine[] = [
      { kind: "removed", oldLine: 3, text: "a" },
      { kind: "added", newLine: 3, text: "b" },
      { kind: "context", oldLine: 4, newLine: 4, text: "c" },
      { kind: "context", oldLine: 40, newLine: 40, text: "d" },
      { kind: "removed", oldLine: 41, text: "e" },
    ];
    const { api } = diffServer(() => json(200, fileDiff({ lines })));
    renderWithProviders(card([PATH]), { api });

    const diff = await screen.findByRole("region", { name: `Changes in ${PATH}` });

    expect(within(diff).getAllByText("Unchanged lines left out")).toHaveLength(2);
    expect(within(diff).getByText("d").closest("tr")?.previousElementSibling).toHaveTextContent("Unchanged lines left out");
  });

  it("cuts a long diff, says so, and shows the whole file on request", async () => {
    const user = userEvent.setup();
    const { api, asked } = diffServer((_, full) => json(200, fileDiff({ status: "added", lines: manyLines(full ? CUT_AT + 100 : CUT_AT), truncated: !full })));
    renderWithProviders(card([PATH], { status: "added", linesAdded: CUT_AT + 100, linesRemoved: 0 }), { api });

    const cut = await screen.findByRole("region", { name: `Changes in ${PATH}` });

    expect(within(cut).getAllByRole("row")).toHaveLength(CUT_AT + 1);
    expect(screen.getByText(`The diff is cut after ${CUT_AT} lines.`)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Show the whole file" }));

    await waitFor(() => expect(within(screen.getByRole("region", { name: `Changes in ${PATH}` })).getAllByRole("row")).toHaveLength(CUT_AT + 100 + 1));
    expect(asked).toEqual([
      { path: PATH, full: false },
      { path: PATH, full: true },
    ]);
    expect(screen.queryByRole("button", { name: "Show the whole file" })).not.toBeInTheDocument();
    expect(screen.queryByText(/The diff is cut/)).not.toBeInTheDocument();
  });

  it("shows hidden characters as the escapes the server wrote, and markup as text", async () => {
    const lines: ProposalDiffLine[] = [
      { kind: "removed", oldLine: 1, text: "ab\\u202ecd\\u000d" },
      { kind: "added", newLine: 1, text: "<img src=x onerror=alert(1)> <b>bold</b>" },
    ];
    const { api } = diffServer(() => json(200, fileDiff({ lines })));
    const { container } = renderWithProviders(card([PATH]), { api });

    const diff = await screen.findByRole("region", { name: `Changes in ${PATH}` });

    expect(within(diff).getByText("ab\\u202ecd\\u000d")).toBeInTheDocument();
    expect(within(diff).getByText("<img src=x onerror=alert(1)> <b>bold</b>")).toBeInTheDocument();
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
  });

  it("says when the final newline changes", async () => {
    const { api } = diffServer(() => json(200, fileDiff({ finalNewline: "removed" })));
    renderWithProviders(card([PATH]), { api });

    expect(await screen.findByText("The new file ends without a newline, which the old file had.")).toBeInTheDocument();
  });

  it("says that a file has no changes", async () => {
    const { api } = diffServer(() => json(200, fileDiff({ lines: [] })));
    renderWithProviders(card([PATH]), { api });

    expect(await screen.findByText("The file has no changes.")).toBeInTheDocument();
  });

  it("shows why the file could not be read, and reads it again on request", async () => {
    const user = userEvent.setup();
    let calls = 0;
    const { api } = diffServer(() => (++calls === 1 ? problem(502, "Sidekick could not read the file on the base branch from the host: not found") : json(200, fileDiff())));
    renderWithProviders(card([PATH]), { api });

    expect(await screen.findByRole("alert")).toHaveTextContent("Sidekick could not show this file. Sidekick could not read the file on the base branch from the host: not found");

    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("region", { name: `Changes in ${PATH}` })).toBeInTheDocument();
    expect(calls).toBe(2);
  });

  it("does not ask for the diff of a file the action cannot read", () => {
    const { api, asked } = diffServer();
    renderWithProviders(card([PATH], { diffAvailable: false }), { api });

    expect(screen.getByText(PATH)).toBeInTheDocument();
    expect(screen.getByText(/the action that made the proposal cannot read it from its host/)).toBeInTheDocument();
    expect(asked).toEqual([]);
  });

  it("shows the files of a deleted and a new file by their words", () => {
    const { api } = diffServer();
    renderWithProviders(
      <ProposalCard
        proposal={proposal({ files: [proposalFile({ path: "a.yaml", status: "added", linesAdded: 3, linesRemoved: 0 }), proposalFile({ path: "b.yaml", status: "deleted", linesAdded: 0, linesRemoved: 7 })] })}
        canDecide
      />,
      { api },
    );

    expect(screen.getByText("Added")).toBeInTheDocument();
    expect(screen.getByText("+3 -0")).toBeInTheDocument();
    expect(screen.getByText("Deleted")).toBeInTheDocument();
    expect(screen.getByText("+0 -7")).toBeInTheDocument();
  });
});
