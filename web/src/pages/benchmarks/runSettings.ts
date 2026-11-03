import type { BenchConnector, BenchRun, BenchRunSummary } from "../../api/bench";
import { formatTime } from "../../format";

/** What is known of the extra settings of a run, which only `GET /bench/runs/{id}` has. */
export type RunDetail = { state: "loading" } | { state: "failed" } | { state: "loaded"; run: BenchRun };

/** One side of a settings row: the setting as text, or why it is not there yet. */
export type SettingCell = { state: "value"; text: string } | { state: "loading" } | { state: "failed" };

/** Whether the two sides of a row differ, are the same, or could not be compared. */
export type SettingMatch = "differs" | "same" | "pending" | "unavailable" | "unchecked";

/** One row of the settings table. */
export interface SettingRow {
  name: string;
  /** The values are hashes and commits, shown in a monospace font. */
  mono: boolean;
  a: SettingCell;
  b: SettingCell;
  match: SettingMatch;
}

const NONE = "none";
const NOT_USED = "not used";

/**
 * The settings of two runs side by side: what the summaries hold, one row for
 * each role and each strategy of either run, and the prompt versions and
 * config hash of the run details.
 */
export function settingRows(a: BenchRunSummary, b: BenchRunSummary, detailA: RunDetail, detailB: RunDetail): SettingRow[] {
  return [
    textRow("Label", a.label || NONE, b.label || NONE),
    { name: "Started", mono: false, a: valueOf(formatTime(a.startedAt)), b: valueOf(formatTime(b.startedAt)), match: "unchecked" },
    textRow("Sidekick version", a.sidekickVersion, b.sidekickVersion),
    { ...textRow("Git commit", a.gitSha, b.gitSha), mono: true },
    textRow("Scenario set", a.scenarioSet, b.scenarioSet),
    textRow("Repeats", String(a.repeats), String(b.repeats)),
    ...groupRows("Models", "Model for", a.connectors, b.connectors, connectorText),
    ...groupRows("Strategies", "Strategy", a.strategies, b.strategies, (version) => `version ${version}`, " version"),
    detailRow("Prompt version", detailA, detailB, (run) => run.promptVersion),
    detailRow("Judge prompt version", detailA, detailB, (run) => run.judgePromptVersion),
    { ...detailRow("Config hash", detailA, detailB, (run) => run.configHash), mono: true },
  ];
}

/** The detail state of a run from its query. */
export function detailOf(query: { isError: boolean; data: BenchRun | undefined }): RunDetail {
  if (query.isError) {
    return { state: "failed" };
  }

  return query.data ? { state: "loaded", run: query.data } : { state: "loading" };
}

/** A role of a run as "model through connector (provider)". */
export function connectorText(c: BenchConnector): string {
  return `${c.model ?? "no model"} through ${c.connector}${c.provider ? ` (${c.provider})` : ""}`;
}

function textRow(name: string, a: string, b: string): SettingRow {
  return { name, mono: false, a: valueOf(a), b: valueOf(b), match: matchOf(a, b) };
}

/** One row for each key of either run, "not used" where a run lacks it; a single "none" row when neither run has any. */
function groupRows<T>(groupName: string, prefix: string, a: Record<string, T>, b: Record<string, T>, text: (value: T) => string, suffix = ""): SettingRow[] {
  const keys = [...new Set([...Object.keys(a), ...Object.keys(b)])].sort((x, y) => x.localeCompare(y));

  if (keys.length === 0) {
    return [textRow(groupName, NONE, NONE)];
  }

  return keys.map((key) => {
    const left = key in a ? text(a[key] as T) : NOT_USED;
    const right = key in b ? text(b[key] as T) : NOT_USED;

    return textRow(`${prefix} ${key}${suffix}`, left, right);
  });
}

function detailRow(name: string, detailA: RunDetail, detailB: RunDetail, pick: (run: BenchRun) => string): SettingRow {
  const a = cellOf(detailA, pick);
  const b = cellOf(detailB, pick);

  if (a.state === "failed" || b.state === "failed") {
    return { name, mono: false, a, b, match: "unavailable" };
  }

  if (a.state === "loading" || b.state === "loading") {
    return { name, mono: false, a, b, match: "pending" };
  }

  return { name, mono: false, a, b, match: matchOf(a.text, b.text) };
}

function cellOf(detail: RunDetail, pick: (run: BenchRun) => string): SettingCell {
  return detail.state === "loaded" ? valueOf(pick(detail.run)) : { state: detail.state };
}

function valueOf(text: string): SettingCell {
  return { state: "value", text };
}

function matchOf(a: string, b: string): SettingMatch {
  return a === b ? "same" : "differs";
}
