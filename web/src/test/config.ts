import type { EffectiveConfig } from "../api/config";

/** The long text of the budgets note in sampleConfig, which scrolls in a block of its own. */
export const LONG_NOTE = "Spend stays under the daily limit. ".repeat(6).trim();

/**
 * An effective config as GET /config answers: the keys of file sorted as the
 * server sorts them, with nested maps, lists, a duration text, references to
 * Secrets, a key the page has never seen (acme), and one listener and one
 * route added at runtime.
 */
export function sampleConfig(): EffectiveConfig {
  return {
    file: {
      acme: { custom: { deep: ["first", { flag: true }] } },
      budgets: { maxCostUSD: 5, note: LONG_NOTE },
      database: { urlSecret: { name: "sidekick-db", key: "url" }, pool: { maxConns: 10, idleTimeout: "10m0s" } },
      model: { provider: "anthropic", apiKeySecret: { name: "sidekick-anthropic", key: "apiKey" }, fallback: null },
      sources: { prod: { type: "kubernetes", namespaces: ["payments", "checkout"] } },
    },
    runtime: {
      listeners: [{ name: "ops", enabled: true, version: 2, updatedBy: "U01SEED", updatedAt: "2026-10-20T10:00:00Z", config: { channel: "#ops", source: "grafana" } }],
      routes: [{ name: "checkout", enabled: false, version: 3, updatedBy: "U02OTHER", updatedAt: "2026-10-21T11:30:00Z", config: { priority: 20, strategy: "latency" } }],
    },
  };
}
