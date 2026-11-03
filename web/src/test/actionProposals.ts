import type { Proposal, ProposalDiffLine, ProposalFile, ProposalFileDiff } from "../api/proposals";
import type { Me } from "../session";
import { VIEWER } from "./helpers";

export const APPROVER: Me = { ...VIEWER, role: "approver" };

export const PROPOSAL_ID = "0192d3a4-0000-7000-8000-0000000000f1";
export const PROPOSAL_INVESTIGATION = "0192d3a4-0000-7000-8000-0000000000e2";

/** A proposal that waits for a decision. */
export function proposal(over: Partial<Proposal> = {}): Proposal {
  return {
    id: PROPOSAL_ID,
    investigationId: PROPOSAL_INVESTIGATION,
    alertTitle: "checkout-api p99 latency above 2s",
    route: "payments",
    action: "monitor-note",
    params: { board: "ops", title: "Slow checkout" },
    paramsText: '{\n  "board": "ops",\n  "title": "Slow checkout"\n}',
    reason: "p99 doubled after the deploy.",
    mode: "approved",
    status: "proposed",
    proposedAt: "2026-10-26T14:02:00Z",
    expiresAt: "2026-10-27T14:02:00Z",
    undoable: false,
    ...over,
  };
}

/** A file of a pull request proposal, whose diff can be asked for. */
export function proposalFile(over: Partial<ProposalFile> = {}): ProposalFile {
  return { path: "prometheus/rules/checkout.yaml", status: "changed", linesAdded: 1, linesRemoved: 1, diffAvailable: true, ...over };
}

/** The diff of a file: one line changed, with a line before and after it. */
export function fileDiff(over: Partial<ProposalFileDiff> = {}): ProposalFileDiff {
  const lines: ProposalDiffLine[] = [
    { kind: "context", oldLine: 4, newLine: 4, text: "  - alert: Slow" },
    { kind: "removed", oldLine: 5, text: "    for: 1m" },
    { kind: "added", newLine: 5, text: "    for: 10m" },
    { kind: "context", oldLine: 6, newLine: 6, text: "  - alert: Errors" },
  ];

  return { path: "prometheus/rules/checkout.yaml", status: "changed", lines, truncated: false, ...over };
}

/** The pull request of a proposal, open and read on 2 November 2026. */
export function pullRequest(over: Partial<NonNullable<Proposal["pullRequest"]>> = {}): NonNullable<Proposal["pullRequest"]> {
  return {
    repo: "acme/monitors",
    number: 12,
    url: "https://github.com/acme/monitors/pull/12",
    state: "open",
    checkedAt: "2026-11-02T06:00:00Z",
    ...over,
  };
}
