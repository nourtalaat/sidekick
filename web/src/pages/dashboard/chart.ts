/** One point of a chart: its label on the x axis and a value for each series key. */
export type ChartRow = { x: string } & Record<string, string | number | undefined>;

/** One series of a chart. The color is a design token, such as "var(--sk-chart-1)". */
export interface ChartSeries {
  key: string;
  label: string;
  color: string;
}

export type ChartKind = "stack" | "bar" | "line";

/** What a chart shows. */
export interface ChartSpec {
  kind: ChartKind;
  data: ChartRow[];
  series: ChartSeries[];
  /** The heading of the first column of the table view. */
  xLabel: string;
  /** Writes a value for the axis, the tooltip and the table. */
  format: (value: number) => string;
  /** A dashed line across the chart, such as the daily cap. */
  referenceLine?: { value: number; label: string };
  /** The range of the y axis; automatic when absent. */
  domain?: [number, number];
  /** More columns for the table view than the series, such as the verdicts that share a color. */
  tableColumns?: { key: string; label: string }[];
}

/** The categorical palette of the design language, in the order series take it. */
export const CATEGORY_COLORS = [
  "var(--sk-chart-1)",
  "var(--sk-chart-2)",
  "var(--sk-chart-3)",
  "var(--sk-chart-4)",
  "var(--sk-chart-5)",
  "var(--sk-chart-6)",
  "var(--sk-chart-7)",
  "var(--sk-chart-8)",
] as const;

/** The color of the palette's slot i, counting from 0; the slots are not cycled, so a slot past the palette gets the color of "Other". */
export function categoryColor(slot: number): string {
  return CATEGORY_COLORS[slot] ?? OTHER_COLOR;
}

/** The color of "Other", the series that the ones past the palette fold into. */
export const OTHER_COLOR = "var(--sk-text-secondary)";

/** The verdict series in the fixed order every verdict chart uses. Duplicate and known issue share one. */
export const VERDICT_SERIES: readonly ChartSeries[] = [
  { key: "true_positive", label: "Real issue", color: "var(--sk-verdict-real-solid)" },
  { key: "flapping", label: "Flapping", color: "var(--sk-verdict-flapping-solid)" },
  { key: "duplicate_or_known", label: "Duplicate or known issue", color: "var(--sk-verdict-duplicate-solid)" },
  { key: "false_positive", label: "False alarm", color: "var(--sk-verdict-false-solid)" },
  { key: "inconclusive", label: "Not sure yet", color: "var(--sk-verdict-unsure-solid)" },
];

/** Whether a chart has anything to draw: a line has a value, a bar has a value above zero. */
export function hasData(spec: ChartSpec): boolean {
  const drawn = (value: string | number | undefined) => typeof value === "number" && (spec.kind === "line" || value > 0);

  return spec.data.some((row) => spec.series.some((s) => drawn(row[s.key])));
}
