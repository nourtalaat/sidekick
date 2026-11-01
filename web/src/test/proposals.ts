import type { Proposal } from "../api/proposals";
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
    ...over,
  };
}
