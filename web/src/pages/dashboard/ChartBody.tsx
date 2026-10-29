import { Bar, BarChart, CartesianGrid, Line, LineChart, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import type { ChartSpec } from "./chart";

const CHART_HEIGHT = 280;
const INITIAL_WIDTH = 640;
const DASH = "6 4";
const LINE_WIDTH = 2;
const DOT_RADIUS = 3;

const TOOLTIP_TEXT = { color: "var(--sk-text-primary)" };
const AXIS_TICK = { fill: "var(--sk-chart-axis)" };
const TOOLTIP_BOX = {
  background: "var(--sk-bg-raised)",
  border: "1px solid var(--sk-border-subtle)",
  borderRadius: "var(--sk-radius-sm)",
  color: "var(--sk-text-primary)",
};

/**
 * The drawing of a chart: stacked or plain bars, or lines, with a grid,
 * axes, a tooltip and an optional dashed reference line. It is the only
 * part of the dashboard that needs the chart library, so the page loads it
 * on demand. The text, legend and table view are in ChartCard.
 */
export default function ChartBody({ spec }: { spec: ChartSpec }) {
  const { kind, data, series, format, referenceLine, domain } = spec;

  const common = (
    <>
      <CartesianGrid stroke="var(--sk-chart-grid)" vertical={false} />
      <XAxis dataKey="x" stroke="var(--sk-chart-axis)" tick={AXIS_TICK} tickLine={false} minTickGap={16} />
      <YAxis allowDecimals stroke="var(--sk-chart-axis)" tick={AXIS_TICK} tickLine={false} tickFormatter={format} domain={domain} width={64} />
      <Tooltip
        cursor={{ fill: "var(--sk-bg-hover)" }}
        contentStyle={TOOLTIP_BOX}
        itemStyle={TOOLTIP_TEXT}
        labelStyle={TOOLTIP_TEXT}
        formatter={(value) => (typeof value === "number" ? format(value) : String(value))}
      />
      {referenceLine && (
        <ReferenceLine
          y={referenceLine.value}
          stroke="var(--sk-text-secondary)"
          strokeDasharray={DASH}
          label={{ value: referenceLine.label, position: "insideTopRight", fill: "var(--sk-text-secondary)" }}
        />
      )}
    </>
  );

  return (
    <ResponsiveContainer width="100%" height={CHART_HEIGHT} initialDimension={{ width: INITIAL_WIDTH, height: CHART_HEIGHT }}>
      {kind === "line" ? (
        <LineChart data={data} accessibilityLayer>
          {common}
          {series.map((s) => (
            <Line
              key={s.key}
              type="monotone"
              dataKey={s.key}
              name={s.label}
              stroke={s.color}
              strokeWidth={LINE_WIDTH}
              dot={{ r: DOT_RADIUS, fill: s.color, stroke: s.color }}
              isAnimationActive={false}
            />
          ))}
        </LineChart>
      ) : (
        <BarChart data={data} accessibilityLayer>
          {common}
          {series.map((s) => (
            <Bar
              key={s.key}
              dataKey={s.key}
              name={s.label}
              stackId={kind === "stack" ? "stack" : undefined}
              fill={s.color}
              stroke="var(--sk-bg-surface)"
              strokeWidth={LINE_WIDTH}
              isAnimationActive={false}
            />
          ))}
        </BarChart>
      )}
    </ResponsiveContainer>
  );
}
