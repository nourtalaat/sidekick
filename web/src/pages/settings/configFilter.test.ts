import { describe, expect, it } from "vitest";
import type { RuntimeRule } from "../../api/config";
import { filterEntries, filterRules, filterValue, isSecretRef } from "./configFilter";

const CONFIG = {
  model: { provider: "anthropic", timeout: "10m0s", apiKeySecret: { name: "sidekick-anthropic", key: "apiKey" } },
  sources: { prod: { type: "kubernetes", namespaces: ["payments", "checkout"] }, logs: { type: "elasticsearch" } },
  budgets: { maxCostUSD: 5 },
};

describe("isSecretRef", () => {
  it.each([
    { name: "a reference", value: { name: "db", key: "url" }, want: true },
    { name: "keys in the other order", value: { key: "url", name: "db" }, want: true },
    { name: "an extra key", value: { name: "db", key: "url", value: "x" }, want: false },
    { name: "a missing key", value: { name: "db" }, want: false },
    { name: "a key that is not text", value: { name: "db", key: 3 }, want: false },
    { name: "a list", value: ["name", "key"], want: false },
    { name: "null", value: null, want: false },
  ])("says $want for $name", ({ value, want }) => {
    expect(isSecretRef(value)).toBe(want);
  });
});

describe("filterEntries", () => {
  it("keeps every entry in order for an empty needle", () => {
    expect(filterEntries(CONFIG, "").map(([key]) => key)).toEqual(["model", "sources", "budgets"]);
  });

  it("keeps a whole entry when its key matches, ignoring case", () => {
    expect(filterEntries(CONFIG, "BUDGETS")).toEqual([["budgets", { maxCostUSD: 5 }]]);
  });

  it("narrows an entry to the keys and values that match", () => {
    expect(filterEntries(CONFIG, "kubernetes")).toEqual([["sources", { prod: { type: "kubernetes" } }]]);
  });

  it("matches the items of a list", () => {
    expect(filterEntries(CONFIG, "check")).toEqual([["sources", { prod: { namespaces: ["checkout"] } }]]);
  });

  it("matches a key inside a nested map and keeps its value", () => {
    expect(filterEntries(CONFIG, "timeout")).toEqual([["model", { timeout: "10m0s" }]]);
  });

  it("matches a number and a duration text", () => {
    expect(filterEntries(CONFIG, "10m")).toEqual([["model", { timeout: "10m0s" }]]);
    expect(filterEntries(CONFIG, "5")).toEqual([["budgets", { maxCostUSD: 5 }]]);
  });

  it("matches a secret reference by its name or key", () => {
    expect(filterEntries(CONFIG, "sidekick-anthropic")).toEqual([["model", { apiKeySecret: { name: "sidekick-anthropic", key: "apiKey" } }]]);
  });

  it("takes the needle as plain text, not as a pattern", () => {
    expect(filterEntries(CONFIG, "kube.*")).toEqual([]);
  });

  it("returns nothing when nothing matches", () => {
    expect(filterEntries(CONFIG, "zzz")).toEqual([]);
  });
});

describe("filterValue", () => {
  it("does not match null", () => {
    expect(filterValue({ a: null }, "null")).toBeUndefined();
  });
});

function rule(over: Partial<RuntimeRule>): RuntimeRule {
  return { name: "checkout", enabled: true, version: 1, updatedBy: "U01SEED", updatedAt: "2026-10-20T10:00:00Z", config: { priority: 20, strategy: "latency" }, ...over };
}

describe("filterRules", () => {
  const rules = [rule({}), rule({ name: "billing", updatedBy: "U02OTHER", config: { priority: 5, strategy: "errors" } })];

  it("keeps every rule for an empty needle", () => {
    expect(filterRules(rules, "")).toEqual(rules);
  });

  it("keeps a rule whole when its name matches", () => {
    expect(filterRules(rules, "BILL")).toEqual([rules[1]]);
  });

  it("keeps a rule whole when its last editor matches", () => {
    expect(filterRules(rules, "u02")).toEqual([rules[1]]);
  });

  it("narrows the config of a rule that matches on a value", () => {
    expect(filterRules(rules, "errors")).toEqual([{ ...rules[1], config: { strategy: "errors" } }]);
  });

  it("returns nothing when nothing matches", () => {
    expect(filterRules(rules, "zzz")).toEqual([]);
  });
});
