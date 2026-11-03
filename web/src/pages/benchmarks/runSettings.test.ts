import { describe, expect, it } from "vitest";
import { benchRun, benchRunDetail } from "../../test/bench";
import { connectorText, detailOf, settingRows, type RunDetail, type SettingRow } from "./runSettings";

const LOADED_A: RunDetail = { state: "loaded", run: benchRunDetail(1) };
const LOADED_B: RunDetail = { state: "loaded", run: benchRunDetail(2) };

function row(rows: SettingRow[], name: string): SettingRow {
  const found = rows.find((r) => r.name === name);
  if (!found) {
    throw new Error(`no row ${name}`);
  }

  return found;
}

describe("settingRows", () => {
  it("has the rows of the summaries and of the details, in order", () => {
    const rows = settingRows(benchRun(1), benchRun(2), LOADED_A, LOADED_B);

    expect(rows.map((r) => r.name)).toEqual([
      "Label",
      "Started",
      "Sidekick version",
      "Git commit",
      "Scenario set",
      "Repeats",
      "Model for investigate",
      "Strategies",
      "Prompt version",
      "Judge prompt version",
      "Config hash",
    ]);
  });

  it("marks the settings that differ and not the ones that match", () => {
    const rows = settingRows(benchRun(1, { label: "a", gitSha: "1111111", repeats: 2 }), benchRun(2, { label: "b", gitSha: "1111111", repeats: 3 }), LOADED_A, LOADED_B);

    expect(row(rows, "Label").match).toBe("differs");
    expect(row(rows, "Git commit").match).toBe("same");
    expect(row(rows, "Git commit").mono).toBe(true);
    expect(row(rows, "Repeats").match).toBe("differs");
    expect(row(rows, "Started").match).toBe("unchecked");
  });

  it("has a row for each role and strategy of either run, and a run without one shows not used", () => {
    const a = benchRun(1, { connectors: { investigate: { connector: "claude", model: "m1" } }, strategies: { "generic-alert": "1", "latency-alert": "2" } });
    const b = benchRun(2, { connectors: { investigate: { connector: "claude", model: "m1" }, judge: { connector: "openai", provider: "azure" } }, strategies: { "latency-alert": "3", "disk-alert": "1" } });
    const rows = settingRows(a, b, LOADED_A, LOADED_B);

    expect(row(rows, "Model for investigate").match).toBe("same");
    expect(row(rows, "Model for judge")).toMatchObject({ a: { state: "value", text: "not used" }, b: { state: "value", text: "no model through openai (azure)" }, match: "differs" });
    expect(rows.filter((r) => r.name.startsWith("Strategy ")).map((r) => [r.name, r.match])).toEqual([
      ["Strategy disk-alert version", "differs"],
      ["Strategy generic-alert version", "differs"],
      ["Strategy latency-alert version", "differs"],
    ]);
    expect(row(rows, "Strategy generic-alert version").b).toEqual({ state: "value", text: "not used" });
  });

  it("has one row saying none when neither run has a role or a strategy", () => {
    const rows = settingRows(benchRun(1, { connectors: {} }), benchRun(2, { connectors: {} }), LOADED_A, LOADED_B);

    expect(row(rows, "Models")).toMatchObject({ a: { text: "none" }, b: { text: "none" }, match: "same" });
    expect(row(rows, "Strategies").match).toBe("same");
  });

  it("compares the prompt versions and the config hash of the details", () => {
    const other: RunDetail = { state: "loaded", run: benchRunDetail(2, { promptVersion: "p-4", configHash: "ffff" }) };
    const rows = settingRows(benchRun(1), benchRun(2), LOADED_A, other);

    expect(row(rows, "Prompt version").match).toBe("differs");
    expect(row(rows, "Judge prompt version").match).toBe("same");
    expect(row(rows, "Config hash")).toMatchObject({ match: "differs", mono: true });
  });

  it.each([
    ["one detail is loading", { state: "loading" }, LOADED_B, "pending"],
    ["both details are loading", { state: "loading" }, { state: "loading" }, "pending"],
    ["one detail failed", LOADED_A, { state: "failed" }, "unavailable"],
    ["one detail failed and the other is loading", { state: "loading" }, { state: "failed" }, "unavailable"],
  ] satisfies [string, RunDetail, RunDetail, string][])("cannot compare the detail rows when %s", (_name, detailA, detailB, match) => {
    const rows = settingRows(benchRun(1), benchRun(2), detailA, detailB);

    for (const name of ["Prompt version", "Judge prompt version", "Config hash"]) {
      expect(row(rows, name).match).toBe(match);
    }

    expect(row(rows, "Sidekick version").match).toBe("same");
  });

  it("carries the state of a detail that is not there into its cell", () => {
    const rows = settingRows(benchRun(1), benchRun(2), { state: "loading" }, { state: "failed" });

    expect(row(rows, "Config hash").a).toEqual({ state: "loading" });
    expect(row(rows, "Config hash").b).toEqual({ state: "failed" });
  });
});

describe("detailOf", () => {
  it("says loaded with data, failed with an error, and loading otherwise", () => {
    const run = benchRunDetail(1);

    expect(detailOf({ isError: false, data: run })).toEqual({ state: "loaded", run });
    expect(detailOf({ isError: true, data: undefined })).toEqual({ state: "failed" });
    expect(detailOf({ isError: false, data: undefined })).toEqual({ state: "loading" });
  });
});

describe("connectorText", () => {
  it.each([
    [{ connector: "claude", model: "big-model", provider: "anthropic" }, "big-model through claude (anthropic)"],
    [{ connector: "openai", model: "small-model" }, "small-model through openai"],
    [{ connector: "openai" }, "no model through openai"],
  ])("words %j", (connector, want) => {
    expect(connectorText(connector)).toBe(want);
  });
});
