import type { Investigation, InvestigationSummary, Step, Watcher } from "../api/investigations";
import type { Me } from "../session";
import { VIEWER } from "./helpers";

export const RESPONDER: Me = { ...VIEWER, id: "u2", name: "Grace Hopper", role: "responder", csrfToken: "csrf-grace" };

export const INVESTIGATION_ID = "0192d3a4-0000-7000-8000-0000000000c1";

/** A row of the list. */
export function summary(overrides: Partial<InvestigationSummary> = {}): InvestigationSummary {
  return {
    id: INVESTIGATION_ID,
    title: "Checkout p99 latency above 2s",
    service: "checkout-api",
    route: "payments",
    verdict: "true_positive",
    confidence: "high",
    status: "final",
    createdAt: "2026-10-26T14:00:01Z",
    startedAt: "2026-10-26T14:00:05Z",
    finishedAt: "2026-10-26T14:03:05Z",
    durationMs: 180_000,
    costMicros: 12_000,
    feedback: { correct: 2, wrong: 0, summary: "correct" },
    watcher: { name: "health", status: "recovered" },
    ...overrides,
  };
}

/** The detail of a finished investigation with a report. */
export function detail(overrides: Partial<Investigation> = {}): Investigation {
  return {
    id: INVESTIGATION_ID,
    groupId: "0192d3a4-0000-7000-8000-0000000000b1",
    groupStatus: "open",
    title: "Checkout p99 latency above 2s",
    service: "checkout-api",
    route: "payments",
    strategy: "generic-alert",
    strategyVersion: "v1",
    mode: "read-only",
    status: "final",
    run: 1,
    runKind: "investigate",
    verdict: "true_positive",
    confidence: "high",
    stopReason: "submitted",
    model: "big-model",
    inputTokens: 1200,
    outputTokens: 300,
    costMicros: 12_000,
    toolCalls: 4,
    createdAt: "2026-10-26T14:00:01Z",
    startedAt: "2026-10-26T14:00:05Z",
    finishedAt: "2026-10-26T14:03:05Z",
    durationMs: 180_000,
    eventCount: 2,
    events: [
      {
        id: "e1",
        kind: "alert",
        intake: "alertmanager",
        title: "Checkout p99 latency above 2s",
        body: "p99 is 2.6 s.",
        sender: { kind: "bot", name: "Alertmanager" },
        labels: {},
        links: [
          { title: "Runbook", url: "https://runbooks.example.com/checkout" },
          { title: "Bad link", url: "javascript:alert(1)" },
        ],
        receivedAt: "2026-10-26T14:00:00Z",
      },
    ],
    pendingFollowUps: [],
    report: {
      verdict: "true_positive",
      confidence: "high",
      reason: "A deploy caused it.",
      summary: "Deploy 4f2c1a doubled latency.",
      cause: "Deploy 4f2c1a",
      impact: "Checkout is slow.",
      findings: [
        {
          id: "f1",
          claim: "Errors rose at 14:02.",
          evidence: [
            { kind: "metric", title: "SigNoz dashboard", url: "https://signoz.example.com/d/1" },
            { kind: "other", title: "Local file", url: "javascript:alert(1)" },
          ],
        },
        { id: "f2", claim: "The deploy caused it.", evidence: [] },
      ],
      fixes: [{ id: "x1", title: "Roll back 4f2c1a", detail: "Revert the deploy." }],
      checked: [
        { source: "signoz", status: "ok", toolCalls: 2 },
        { source: "github", status: "unavailable", toolCalls: 0, error: "did not respond" },
      ],
      stopReason: "submitted",
    },
    ...overrides,
  };
}

/** A step of the record. */
export function step(seq: number, overrides: Partial<Step> = {}): Step {
  return {
    seq,
    run: 1,
    kind: "model_turn",
    status: "ok",
    startedAt: "2026-10-26T14:00:06Z",
    durationMs: 1200,
    tokens: { input: 100, output: 20 },
    parts: [{ type: "text", text: `Thinking about step ${seq}` }],
    ...overrides,
  };
}

/** A watcher with its checks. */
export function watcher(overrides: Partial<Watcher> = {}): Watcher {
  return {
    id: "0192d3a4-0000-7000-8000-0000000000d2",
    name: "health",
    checkType: "http",
    status: "running",
    passesInARow: 0,
    passesToRecover: 2,
    intervalSeconds: 30,
    checks: 1,
    lastCheckAt: "2026-10-26T14:21:00Z",
    lastResult: "HTTP 503",
    startedAt: "2026-10-26T14:20:00Z",
    recentChecks: [],
    ...overrides,
  };
}
