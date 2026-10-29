import type { StatsQuery, Verdict } from "../../api/stats";

const MS_PER_DAY = 86_400_000;
const ISO_DATE_LENGTH = 10;

/** The longest range the API takes, in days. */
export const MAX_RANGE_DAYS = 400;

/** The ranges the dashboard offers, ending today. */
export const RANGE_PRESETS: readonly { value: string; label: string; days: number }[] = [
  { value: "7d", label: "Last 7 days", days: 7 },
  { value: "30d", label: "Last 30 days", days: 30 },
  { value: "90d", label: "Last 90 days", days: 90 },
  { value: "365d", label: "Last 365 days", days: 365 },
];

/** The value of the range parameter for dates the person picked. */
export const CUSTOM_RANGE = "custom";

/** The range when the URL names none. */
export const DEFAULT_RANGE = "30d";

/** The verdicts the filter offers, with the labels the badges use. */
export const VERDICT_OPTIONS: readonly { value: Verdict; label: string }[] = [
  { value: "true_positive", label: "Real issue" },
  { value: "false_positive", label: "False alarm" },
  { value: "inconclusive", label: "Not sure yet" },
  { value: "duplicate", label: "Duplicate" },
  { value: "known_issue", label: "Known issue" },
  { value: "flapping", label: "Flapping" },
];

/** What the URL of the dashboard keeps: the range, the filters and the two benchmark runs to compare. */
export interface DashboardFilters {
  /** A preset such as "30d", or CUSTOM_RANGE with from and to. */
  range: string;
  /** The first and last day of a custom range, as YYYY-MM-DD. */
  from?: string;
  to?: string;
  route?: string;
  service?: string;
  model?: string;
  verdict?: Verdict;
  /** The IDs of the benchmark runs to compare. */
  compareA?: string;
  compareB?: string;
}

const TEXT_PARAMS = ["route", "service", "model"] as const;
const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;
const ID_PATTERN = /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i;

/** The filters in a URL's query. A value the API would refuse is left out, so every link opens. */
export function filtersFromParams(params: URLSearchParams): DashboardFilters {
  const filters: DashboardFilters = { range: DEFAULT_RANGE };

  const range = params.get("range");
  if (RANGE_PRESETS.some((preset) => preset.value === range)) {
    filters.range = range as string;
  } else if (range === CUSTOM_RANGE) {
    const from = params.get("from") ?? "";
    const to = params.get("to") ?? "";

    if (validDays(from, to)) {
      Object.assign(filters, { range: CUSTOM_RANGE, from, to });
    }
  }

  for (const name of TEXT_PARAMS) {
    const value = params.get(name)?.trim();
    if (value) {
      filters[name] = value;
    }
  }

  const verdict = VERDICT_OPTIONS.find((option) => option.value === params.get("verdict"));
  if (verdict) {
    filters.verdict = verdict.value;
  }

  for (const [param, key] of [
    ["a", "compareA"],
    ["b", "compareB"],
  ] as const) {
    const id = params.get(param);
    if (id && ID_PATTERN.test(id)) {
      filters[key] = id;
    }
  }

  return filters;
}

/** The URL query for filters. The default range and the empty filters are left out. */
export function paramsFromFilters(filters: DashboardFilters): URLSearchParams {
  const params = new URLSearchParams();

  if (filters.range === CUSTOM_RANGE && filters.from && filters.to) {
    params.set("range", CUSTOM_RANGE);
    params.set("from", filters.from);
    params.set("to", filters.to);
  } else if (filters.range !== DEFAULT_RANGE) {
    params.set("range", filters.range);
  }

  for (const name of TEXT_PARAMS) {
    const value = filters[name];
    if (value) {
      params.set(name, value);
    }
  }

  if (filters.verdict) {
    params.set("verdict", filters.verdict);
  }

  if (filters.compareA) {
    params.set("a", filters.compareA);
  }

  if (filters.compareB) {
    params.set("b", filters.compareB);
  }

  return params;
}

/** The query of the stats requests for filters, with the presets ending on the UTC date of now. */
export function statsQueryOf(filters: DashboardFilters, now: Date): StatsQuery {
  const query: StatsQuery = { ...rangeOf(filters, now) };

  for (const name of TEXT_PARAMS) {
    const value = filters[name];
    if (value) {
      query[name] = value;
    }
  }

  if (filters.verdict) {
    query.verdict = filters.verdict;
  }

  return query;
}

/** The first and last day of the range of filters. */
export function rangeOf(filters: DashboardFilters, now: Date): { from: string; to: string } {
  if (filters.range === CUSTOM_RANGE && filters.from && filters.to) {
    return { from: filters.from, to: filters.to };
  }

  const days = RANGE_PRESETS.find((preset) => preset.value === filters.range)?.days ?? 30;
  const to = utcDate(now);

  return { from: utcDate(new Date(now.getTime() - (days - 1) * MS_PER_DAY)), to };
}

/** The UTC date of a time, as YYYY-MM-DD. */
export function utcDate(time: Date): string {
  return time.toISOString().slice(0, ISO_DATE_LENGTH);
}

/** Whether from and to are dates, in order, at most MAX_RANGE_DAYS days apart. */
function validDays(from: string, to: string): boolean {
  if (!DATE_PATTERN.test(from) || !DATE_PATTERN.test(to)) {
    return false;
  }

  const start = Date.parse(`${from}T00:00:00Z`);
  const end = Date.parse(`${to}T00:00:00Z`);

  return !Number.isNaN(start) && !Number.isNaN(end) && start <= end && (end - start) / MS_PER_DAY < MAX_RANGE_DAYS;
}
