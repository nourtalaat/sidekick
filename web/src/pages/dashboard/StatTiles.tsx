import type { CostStats, OperationsStats, QualityStats } from "../../api/stats";
import { formatCost, formatDuration } from "../../format";
import { countOf } from "./data";
import { capUsed, costChange, countChange, durationChange, formatCount, formatShare, shareChange } from "./words";
import "./Dashboard.css";

interface Tile {
  label: string;
  value: string;
  /** The change against the range before, in words. */
  change: string;
}

interface StatTilesProps {
  quality: QualityStats | undefined;
  operations: OperationsStats | undefined;
  cost: CostStats | undefined;
}

const WAITING = "Loading";

/**
 * The four numbers at the top: verdict accuracy from feedback, false alarms
 * found, the median time to the first finding, and today's spend against the
 * daily cap. Each is a number, a label and the change against the range
 * before it, in words.
 */
export function StatTiles({ quality, operations, cost }: StatTilesProps) {
  const tiles: Tile[] = [accuracyTile(quality), falseAlarmTile(operations), firstFindingTile(operations), spendTile(cost)];

  return (
    <section aria-label="Key numbers">
      <ul className="sk-tiles">
        {tiles.map((tile) => (
          <li key={tile.label} className="sk-tile">
            <p className="sk-tile__label">{tile.label}</p>
            <p className="sk-tile__value">{tile.value}</p>
            <p className="sk-tile__change">{tile.change}</p>
          </li>
        ))}
      </ul>
    </section>
  );
}

function accuracyTile(quality: QualityStats | undefined): Tile {
  const label = "Verdict accuracy";

  if (!quality) {
    return { label, value: WAITING, change: "" };
  }

  return {
    label,
    value: formatShare(quality.totals.accuracy),
    change: `${shareChange(quality.totals.accuracy, quality.previous.accuracy)}, from ${formatCount(quality.totals.rated)} rated`,
  };
}

function falseAlarmTile(operations: OperationsStats | undefined): Tile {
  const label = "False alarms found";

  if (!operations) {
    return { label, value: WAITING, change: "" };
  }

  const now = countOf(operations.verdicts, "false_positive");

  return { label, value: formatCount(now), change: countChange(now, countOf(operations.previousVerdicts, "false_positive")) };
}

function firstFindingTile(operations: OperationsStats | undefined): Tile {
  const label = "Median time to first finding";

  if (!operations) {
    return { label, value: WAITING, change: "" };
  }

  const median = operations.totals.medianFirstFindingMs;

  return {
    label,
    value: median === undefined ? "none yet" : `about ${formatDuration(median)}`,
    change: durationChange(median, operations.previous.medianFirstFindingMs),
  };
}

function spendTile(cost: CostStats | undefined): Tile {
  const label = "Spend today";

  if (!cost) {
    return { label, value: WAITING, change: "" };
  }

  const { today } = cost;
  const yesterday = cost.days.find((d) => d.day === dayBefore(today.day));
  const against = today.capMicros ? `${formatCost(today.spentMicros)} of ${formatCost(today.capMicros)}` : formatCost(today.spentMicros);
  const used = today.capMicros ? `${capUsed(today.spentMicros, today.capMicros)}, ` : "";

  return {
    label,
    value: against,
    change: yesterday ? `${used}${costChange(today.spentMicros, yesterday.costMicros).toLowerCase()} from yesterday` : `${used}no figure for yesterday`,
  };
}

const MS_PER_DAY = 86_400_000;
const ISO_DATE_LENGTH = 10;

/** The UTC date before a date, as YYYY-MM-DD. */
function dayBefore(date: string): string {
  return new Date(Date.parse(`${date}T00:00:00Z`) - MS_PER_DAY).toISOString().slice(0, ISO_DATE_LENGTH);
}
