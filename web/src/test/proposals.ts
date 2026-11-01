import type { components } from "../api/schema";
import { json, problem, type Handler } from "./helpers";
import { codedProblem, recording, validationProblem, type Recorded } from "./rules";

type Summary = components["schemas"]["StrategyProposalSummary"];
type Proposal = components["schemas"]["StrategyProposal"];

export const BASE_YAML = "name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check what changed.\n";
export const PROPOSED_YAML = "name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check what changed.\n  - Ask whether a deploy was planned.\n";
export const PASSED_ID = "01920000-0000-7000-8000-000000000301";
export const FAILED_ID = "01920000-0000-7000-8000-000000000302";
export const FILE_ID = "01920000-0000-7000-8000-000000000306";
export const PENDING_ID = "01920000-0000-7000-8000-000000000303";

/** A passed proposal for a runtime strategy, with a gate result and two wrong ratings. */
export function passedProposal(over: Partial<Proposal> = {}): Proposal {
  return {
    id: PASSED_ID,
    name: "checkout-latency",
    baseVersion: "runtime:2",
    baseOrigin: "runtime",
    rationale: "Planned deploys were reported as incidents.",
    triggerCount: 3,
    status: "passed",
    gateResult: "passed",
    benchRunId: "01920000-0000-7000-8000-000000000501",
    baselineRunId: "01920000-0000-7000-8000-000000000502",
    createdAt: "2026-10-26T10:00:00Z",
    updatedAt: "2026-10-26T10:00:00Z",
    baseBody: BASE_YAML,
    body: PROPOSED_YAML,
    stale: false,
    approvable: true,
    feedback: [
      { id: "f1", investigationId: "inv-1", targetKind: "verdict", targetRef: "run:1/verdict", rating: "wrong", reason: "It was a planned deploy.", updatedAt: "2026-10-26T10:05:00Z" },
      { id: "f2", investigationId: "inv-2", targetKind: "finding", targetRef: "run:1/finding:f1", rating: "wrong", updatedAt: "2026-10-26T10:06:00Z" },
    ],
    gate: {
      result: "passed",
      reason: "Fine.",
      mean: 0.01,
      interval: { lower: -0.005, upper: 0.03 },
      maxDrop: 0.02,
      resamples: 10000,
      seed: "42",
      repeats: 5,
      costMicros: 1_200_000,
      maxCostMicros: 5_000_000,
      scenarios: [
        { id: "latency-a", baseline: 0.8, proposal: 0.82, difference: 0.02 },
        { id: "latency-b", baseline: 0.5, proposal: 0.5, difference: 0 },
      ],
    },
    ...over,
  };
}

/** A proposal whose gate ended with another result than a pass. */
export function failedProposal(result: components["schemas"]["StrategyGateResult"] = "failed", over: Partial<Proposal> = {}): Proposal {
  return passedProposal({
    id: FAILED_ID,
    status: "failed",
    gateResult: result,
    approvable: false,
    gate: { ...(passedProposal().gate as NonNullable<Proposal["gate"]>), result, mean: -0.3, interval: { lower: -0.4, upper: -0.2 } },
    ...over,
  });
}

function summaryOf(p: Proposal): Summary {
  const { baseBody, body, feedback, gate, stale, approvable, ...summary } = p;
  void [baseBody, body, feedback, gate, stale, approvable];

  return summary;
}

interface Content {
  proposals: Proposal[];
  /** Answers an approval; the default marks the proposal applied. */
  approve?: (request: Recorded, p: Proposal) => Response;
}

/** A fake server of the proposal endpoints over content, and the requests it saw. */
export function proposalApi(content: Content): { handler: Handler; seen: Recorded[] } {
  return recording((request) => {
    const path = request.path.replace("/api/v1", "");

    if (path === "/strategy-proposals") {
      const status = request.search.get("status");
      const listed = content.proposals.filter((p) => (status ? p.status === status : !["drafting", "draft_failed"].includes(p.status)));

      return json(200, { proposals: listed.map(summaryOf) });
    }

    const one = /^\/strategy-proposals\/([^/]+)(?:\/(approve|reject))?$/.exec(path);
    if (!one) {
      return problem(404, "Not found.");
    }

    const found = content.proposals.find((p) => p.id === one[1]);
    if (!found) {
      return problem(404, "No strategy proposal has this ID.");
    }

    if (request.method === "GET") {
      return json(200, found);
    }

    if (one[2] === "approve") {
      if (content.approve) {
        return content.approve(request, found);
      }

      if (found.status !== "passed") {
        return codedProblem(409, "not_passed", "This proposal has not passed its benchmark gate.");
      }

      found.status = found.baseOrigin === "file" ? "approved" : "applied";

      return json(200, found.baseOrigin === "file" ? { proposal: summaryOf(found), yaml: found.body } : { proposal: summaryOf(found), version: 3 });
    }

    if (one[2] === "reject") {
      found.status = "rejected";

      return json(200, summaryOf(found));
    }

    // PUT: an edit.
    const body = (request.body as { body: string }).body;
    if (body.includes("nope:")) {
      return validationProblem([{ field: "body.nope", message: "unknown key" }]);
    }

    found.body = body;
    found.status = "pending";
    found.gate = undefined;
    found.approvable = false;

    return json(200, found);
  });
}
