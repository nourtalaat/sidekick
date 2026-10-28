import { useState } from "react";
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { Button } from "../components/Button";
import "./VerdictChart.css";

interface Day {
  day: string;
  real: number;
  flapping: number;
  duplicate: number;
  known: number;
  falseAlarm: number;
  unsure: number;
}

const DAYS = 14;
const CHART_HEIGHT = 280;

/** Sample numbers that look like a fortnight of investigations. */
const SAMPLE: Day[] = Array.from({ length: DAYS }, (_, i) => ({
  day: new Date(Date.UTC(2026, 9, 12 + i)).toISOString().slice(5, 10),
  real: 2 + ((i * 3) % 5),
  flapping: 1 + ((i * 2) % 4),
  duplicate: 2 + ((i * 5) % 4),
  known: 1 + ((i * 3) % 3),
  falseAlarm: 6 + ((i * 7) % 9),
  unsure: (i * 2) % 3,
}));

/** The verdict series in the fixed order every verdict chart uses. Duplicate and known issue share one. */
const SERIES = [
  { key: "real", label: "Real issue", color: "var(--sk-verdict-real-solid)" },
  { key: "flapping", label: "Flapping", color: "var(--sk-verdict-flapping-solid)" },
  { key: "duplicateOrKnown", label: "Duplicate or known issue", color: "var(--sk-verdict-duplicate-solid)" },
  { key: "falseAlarm", label: "False alarm", color: "var(--sk-verdict-false-solid)" },
  { key: "unsure", label: "Not sure yet", color: "var(--sk-verdict-unsure-solid)" },
] as const;

const CHART_DATA = SAMPLE.map((d) => ({ ...d, duplicateOrKnown: d.duplicate + d.known }));

const TABLE_COLUMNS: readonly { key: keyof Omit<Day, "day">; label: string }[] = [
  { key: "real", label: "Real issue" },
  { key: "flapping", label: "Flapping" },
  { key: "duplicate", label: "Duplicate" },
  { key: "known", label: "Known issue" },
  { key: "falseAlarm", label: "False alarm" },
  { key: "unsure", label: "Not sure yet" },
];

const TOOLTIP_TEXT = { color: "var(--sk-text-primary)" };
const AXIS_TICK = { fill: "var(--sk-chart-axis)" };

/**
 * The sample chart: investigations per day by verdict, stacked, in the
 * verdict colors, with a legend, a tooltip, keyboard access to the bars and
 * a table view of the same numbers.
 */
export default function VerdictChart() {
  const [showTable, setShowTable] = useState(false);

  return (
    <figure className="sk-chart">
      <figcaption className="sk-chart__summary">{summary(SAMPLE)}</figcaption>
      <ul className="sk-chart__legend">
        {SERIES.map((s) => (
          <li key={s.key}>
            <svg width="12" height="12" aria-hidden="true">
              <rect width="12" height="12" rx="2" fill={s.color} />
            </svg>
            {s.label}
          </li>
        ))}
      </ul>
      <Button onClick={() => setShowTable(!showTable)}>{showTable ? "Show as chart" : "Show as table"}</Button>
      {showTable ? (
        <div className="sk-chart__table-wrap">
          <table className="sk-chart__table">
            <caption>Investigations per day by verdict, sample data</caption>
            <thead>
              <tr>
                <th scope="col">Day</th>
                {TABLE_COLUMNS.map((c) => (
                  <th key={c.key} scope="col">
                    {c.label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {SAMPLE.map((d) => (
                <tr key={d.day}>
                  <th scope="row">{d.day}</th>
                  {TABLE_COLUMNS.map((c) => (
                    <td key={c.key}>{d[c.key]}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <ResponsiveContainer width="100%" height={CHART_HEIGHT} initialDimension={{ width: 640, height: CHART_HEIGHT }}>
          <BarChart data={CHART_DATA} accessibilityLayer>
            <CartesianGrid stroke="var(--sk-chart-grid)" vertical={false} />
            <XAxis dataKey="day" stroke="var(--sk-chart-axis)" tick={AXIS_TICK} tickLine={false} />
            <YAxis allowDecimals={false} stroke="var(--sk-chart-axis)" tick={AXIS_TICK} tickLine={false} />
            <Tooltip
              cursor={{ fill: "var(--sk-bg-hover)" }}
              contentStyle={{
                background: "var(--sk-bg-raised)",
                border: "1px solid var(--sk-border-subtle)",
                borderRadius: "var(--sk-radius-sm)",
                color: "var(--sk-text-primary)",
              }}
              itemStyle={TOOLTIP_TEXT}
              labelStyle={TOOLTIP_TEXT}
            />
            {SERIES.map((s) => (
              <Bar
                key={s.key}
                dataKey={s.key}
                name={s.label}
                stackId="verdict"
                fill={s.color}
                stroke="var(--sk-bg-surface)"
                strokeWidth={2}
                isAnimationActive={false}
              />
            ))}
          </BarChart>
        </ResponsiveContainer>
      )}
    </figure>
  );
}

/** A sentence that says what the chart shows, for readers who do not see it. */
function summary(days: Day[]): string {
  const total = (pick: (d: Day) => number) => days.reduce((sum, d) => sum + pick(d), 0);
  const all = total((d) => d.real + d.flapping + d.duplicate + d.known + d.falseAlarm + d.unsure);

  return `Sample data for ${days.length} days: ${all} investigations, of which ${total((d) => d.falseAlarm)} were false alarms and ${total((d) => d.real)} real issues.`;
}
