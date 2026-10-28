const MS_PER_SECOND = 1000;
const SECONDS_PER_MINUTE = 60;
const MINUTES_PER_HOUR = 60;
const MICROS_PER_DOLLAR = 1_000_000;
const THOUSAND = 1000;

/** A length of time such as "850 ms", "12 s", "4 min 5 s" or "1 h 3 min". */
export function formatDuration(ms: number | undefined): string {
  if (ms === undefined) {
    return "";
  }

  if (ms < MS_PER_SECOND) {
    return `${Math.round(ms)} ms`;
  }

  const seconds = Math.round(ms / MS_PER_SECOND);
  if (seconds < SECONDS_PER_MINUTE) {
    return `${seconds} s`;
  }

  const minutes = Math.floor(seconds / SECONDS_PER_MINUTE);
  if (minutes < MINUTES_PER_HOUR) {
    const rest = seconds % SECONDS_PER_MINUTE;

    return rest === 0 ? `${minutes} min` : `${minutes} min ${rest} s`;
  }

  const rest = minutes % MINUTES_PER_HOUR;

  return rest === 0 ? `${Math.floor(minutes / MINUTES_PER_HOUR)} h` : `${Math.floor(minutes / MINUTES_PER_HOUR)} h ${rest} min`;
}

/** A cost in millionths of a US dollar, such as "$0.0123" below a dollar and "$1.20" above. */
export function formatCost(micros: number | undefined): string {
  if (micros === undefined) {
    return "";
  }

  const dollars = micros / MICROS_PER_DOLLAR;

  return micros < MICROS_PER_DOLLAR ? `$${dollars.toFixed(4)}` : `$${dollars.toFixed(2)}`;
}

/** A count of tokens such as "950" or "12.3k". */
export function formatTokens(count: number | undefined): string {
  if (count === undefined) {
    return "";
  }

  return count < THOUSAND ? String(count) : `${(count / THOUSAND).toFixed(1)}k`;
}

/** A date and time in the viewer's time zone, such as "Oct 26, 14:02". */
export function formatTime(iso: string | undefined): string {
  if (!iso) {
    return "";
  }

  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "";
  }

  return date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

/** The time of day with seconds, such as "14:13:30". */
export function formatClock(iso: string | undefined): string {
  if (!iso) {
    return "";
  }

  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "";
  }

  return date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}
