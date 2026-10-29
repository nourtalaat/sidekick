import type { StatsFilters } from "../../api/stats";
import { Button } from "../../components/Button";
import { Select, type SelectOption } from "../../components/Select";
import { TextField } from "../../components/TextField";
import { FILTERS_ID } from "./ChartCard";
import { CUSTOM_RANGE, DEFAULT_RANGE, RANGE_PRESETS, rangeOf, VERDICT_OPTIONS, type DashboardFilters } from "./params";
import "./Dashboard.css";

const ANY = "";

interface FilterBarProps {
  filters: DashboardFilters;
  /** The values the menus offer; the menus hold only the current value until they arrive. */
  options: StatsFilters | undefined;
  /** The time that the presets end on, so choosing custom dates starts from the range on screen. */
  now: Date;
  onChange(filters: DashboardFilters): void;
}

/**
 * The filters of the whole page in one row: the time range, route, service,
 * model and verdict. Every control applies at once, and all of them are in
 * the URL, so a view can be shared.
 */
export function FilterBar({ filters, options, now, onChange }: FilterBarProps) {
  const custom = filters.range === CUSTOM_RANGE;
  const range = rangeOf(filters, now);

  const menu = (any: string, values: readonly string[] | undefined, current: string | undefined): SelectOption[] => {
    const names = new Set(values ?? []);
    if (current) {
      names.add(current);
    }

    return [{ value: ANY, label: any }, ...[...names].sort().map((name) => ({ value: name, label: name }))];
  };

  const setDates = (from: string, to: string) => {
    if (from && to) {
      onChange({ ...filters, range: CUSTOM_RANGE, from, to });
    }
  };

  return (
    <form id={FILTERS_ID} tabIndex={-1} role="search" aria-label="Filter the dashboard" className="sk-dashboard__filters" onSubmit={(event) => event.preventDefault()}>
      <Select
        label="Time range"
        value={filters.range}
        options={[...RANGE_PRESETS.map((p) => ({ value: p.value, label: p.label })), { value: CUSTOM_RANGE, label: "Custom dates" }]}
        onChange={(event) =>
          event.target.value === CUSTOM_RANGE
            ? onChange({ ...filters, range: CUSTOM_RANGE, from: range.from, to: range.to })
            : onChange({ ...filters, range: event.target.value, from: undefined, to: undefined })
        }
      />
      {custom && (
        <>
          <TextField label="From (UTC)" type="date" value={filters.from ?? range.from} max={range.to} onChange={(event) => setDates(event.target.value, range.to)} />
          <TextField label="To (UTC)" type="date" value={filters.to ?? range.to} min={range.from} onChange={(event) => setDates(range.from, event.target.value)} />
        </>
      )}
      <Select
        label="Route"
        value={filters.route ?? ANY}
        options={menu("Any route", options?.routes, filters.route)}
        onChange={(event) => onChange({ ...filters, route: event.target.value || undefined })}
      />
      <Select
        label="Service"
        value={filters.service ?? ANY}
        options={menu("Any service", options?.services, filters.service)}
        onChange={(event) => onChange({ ...filters, service: event.target.value || undefined })}
      />
      <Select
        label="Model"
        value={filters.model ?? ANY}
        options={menu("Any model", options?.models, filters.model)}
        onChange={(event) => onChange({ ...filters, model: event.target.value || undefined })}
      />
      <Select
        label="Verdict"
        value={filters.verdict ?? ANY}
        options={[{ value: ANY, label: "Any verdict" }, ...VERDICT_OPTIONS]}
        onChange={(event) => onChange({ ...filters, verdict: VERDICT_OPTIONS.find((o) => o.value === event.target.value)?.value })}
      />
      <Button onClick={() => onChange({ range: DEFAULT_RANGE, compareA: filters.compareA, compareB: filters.compareB })}>Clear</Button>
    </form>
  );
}
