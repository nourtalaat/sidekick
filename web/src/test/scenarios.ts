import type { Scenario, ScenarioDetail } from "../api/scenarios";

/** A scenario of the list, a draft by default. */
export function scenario(over: Partial<Scenario> = {}): Scenario {
  return {
    id: "latency-alert-checkout-0198f2",
    title: "Checkout latency above 2s",
    status: "draft",
    revision: 1,
    investigationId: "0192d3a4-0000-7000-8000-0000000000c1",
    createdAt: "2026-10-30T09:00:00Z",
    updatedAt: "2026-10-30T09:05:00Z",
    ...over,
  };
}

/** A scenario with its review view: one payload, one answer with a kept trace ID. */
export function scenarioDetail(over: Partial<ScenarioDetail> = {}): ScenarioDetail {
  return {
    scenario: scenario(),
    body: "id: latency-alert-checkout-0198f2\ntitle: Checkout latency above 2s\n",
    notes: [{ kind: "unavailable_source", text: "k8s was unavailable in the investigation." }],
    candidates: [{ path: "sources.signoz.fixtures[0].respond.text", start: 6, end: 20, reason: "looks like an opaque ID" }],
    pairs: [
      { ref: "event.payload", label: "Event payload", original: '{"instance": "web-7.prod.acme.com"}', cleaned: '{"instance": "host-1"}' },
      {
        ref: "sources.signoz.fixtures[0].respond.text",
        label: "signoz search_logs: answer 1",
        original: "trace tokXYZ1234567890 on web-7.prod.acme.com",
        cleaned: "trace tokXYZ1234567890 on host-1",
      },
      { ref: "sources.github.fixtures[0].respond.text", label: "github list_commits: answer 1", cleaned: "abc1234 by user-1", note: "The original is no longer kept." },
    ],
    ...over,
  };
}
