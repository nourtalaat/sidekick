import { formatCost, formatDuration } from "../../format";

const PERCENT = 100;

/** A share from 0 to 1 as a percentage, such as "76%"; "none yet" when there is no share. */
export function formatShare(share: number | undefined): string {
  return share === undefined ? "none yet" : `${Math.round(share * PERCENT)}%`;
}

/** A number with thousands separators, such as "1,234". */
export function formatCount(n: number): string {
  return n.toLocaleString("en-US");
}

/**
 * How a share changed from the range before, in points, in words, such as
 * "Up 4 points". Nothing is said when either range had no share.
 */
export function shareChange(current: number | undefined, previous: number | undefined): string {
  if (current === undefined || previous === undefined) {
    return "No figure for the range before";
  }

  const points = Math.round((current - previous) * PERCENT);

  return signed(points, (n) => `${n} ${n === 1 ? "point" : "points"}`);
}

/** How a count changed from the range before, in percent, in words, such as "Down 12 percent". */
export function countChange(current: number, previous: number): string {
  if (previous === 0) {
    return current === 0 ? "No change" : "None in the range before";
  }

  const percent = Math.round(((current - previous) / previous) * PERCENT);

  return signed(percent, (n) => `${n} percent`);
}

/** How a length of time in milliseconds changed from the range before, such as "Up 20 s". */
export function durationChange(current: number | undefined, previous: number | undefined): string {
  if (current === undefined || previous === undefined) {
    return "No figure for the range before";
  }

  const diff = Math.round(current - previous);

  return signed(diff, (n) => formatDuration(n));
}

/** How an amount in micro-dollars changed, such as "Up $0.0020". */
export function costChange(current: number, previous: number): string {
  return signed(Math.round(current - previous), (n) => formatCost(n));
}

/** "Up <amount>", "Down <amount>" or "No change" for a difference. */
function signed(diff: number, amount: (absolute: number) => string): string {
  if (diff === 0) {
    return "No change";
  }

  return `${diff > 0 ? "Up" : "Down"} ${amount(Math.abs(diff))}`;
}

/** A share of a cap used, such as "82% of the cap". */
export function capUsed(spent: number, cap: number): string {
  return `${Math.round((spent / cap) * PERCENT)}% of the cap`;
}
