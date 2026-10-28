import { RANGES, type InvestigationStatus, type ListFilters, type Verdict } from "../../api/investigations";

/** The verdicts the list can filter by, with the labels the badges use. */
export const VERDICT_OPTIONS: readonly { value: Verdict; label: string }[] = [
  { value: "true_positive", label: "Real issue" },
  { value: "false_positive", label: "False alarm" },
  { value: "inconclusive", label: "Not sure yet" },
  { value: "duplicate", label: "Duplicate" },
  { value: "known_issue", label: "Known issue" },
  { value: "flapping", label: "Flapping" },
];

/** The statuses the list can filter by. */
export const STATUS_OPTIONS: readonly { value: InvestigationStatus; label: string }[] = [
  { value: "queued", label: "Queued" },
  { value: "running", label: "Investigating" },
  { value: "final", label: "Done" },
  { value: "failed", label: "Failed" },
  { value: "cancelled", label: "Cancelled" },
];

/** The time ranges the list offers, with their labels. */
export const RANGE_OPTIONS: readonly { value: string; label: string }[] = [
  { value: "1h", label: "Last hour" },
  { value: "24h", label: "Last 24 hours" },
  { value: "7d", label: "Last 7 days" },
  { value: "30d", label: "Last 30 days" },
];

/** The name of the URL parameter that turns compact rows on. */
export const COMPACT_PARAM = "compact";

const TEXT_PARAMS = ["q", "route", "service", "model"] as const;

/** The filters in a URL's query. Values that are not ones the API takes are left out. */
export function filtersFromParams(params: URLSearchParams): ListFilters {
  const filters: ListFilters = {};

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

  const status = STATUS_OPTIONS.find((option) => option.value === params.get("status"));
  if (status) {
    filters.status = status.value;
  }

  const range = params.get("range");
  if (range && range in RANGES) {
    filters.range = range;
  }

  return filters;
}

/** The URL query for filters, with compact rows when compact is set. */
export function paramsFromFilters(filters: ListFilters, compact: boolean): URLSearchParams {
  const params = new URLSearchParams();

  for (const [name, value] of Object.entries(filters)) {
    if (value) {
      params.set(name, value);
    }
  }

  if (compact) {
    params.set(COMPACT_PARAM, "1");
  }

  return params;
}
