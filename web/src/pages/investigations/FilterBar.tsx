import { Button } from "../../components/Button";
import { SelectField } from "../../components/SelectField";
import { TextField } from "../../components/TextField";
import type { ListFilters } from "../../api/investigations";
import { RANGE_OPTIONS, STATUS_OPTIONS, VERDICT_OPTIONS } from "./filters";
import "./FilterBar.css";

const ANY = "";

interface FilterBarProps {
  filters: ListFilters;
  onChange(filters: ListFilters): void;
}

/**
 * The filters in one row. The selects apply at once; the text fields apply
 * when the form is sent, so the list does not reload at every key.
 */
export function FilterBar({ filters, onChange }: FilterBarProps) {
  // What is typed in the text fields now, so a change of a select keeps it.
  const typed = (form: HTMLFormElement | null): Partial<ListFilters> => {
    if (!form) {
      return {};
    }

    const data = new FormData(form);
    const text = (name: string) => {
      const value = data.get(name);

      return typeof value === "string" ? value.trim() || undefined : undefined;
    };

    return { q: text("q"), route: text("route"), service: text("service"), model: text("model") };
  };

  return (
    <form
      role="search"
      aria-label="Filter investigations"
      className="sk-filters"
      // The key makes the text fields start again from the URL when it changes, such as on Clear.
      key={JSON.stringify(filters)}
      onSubmit={(event) => {
        event.preventDefault();
        onChange({ ...filters, ...typed(event.currentTarget) });
      }}
    >
      <TextField label="Alert title" name="q" defaultValue={filters.q ?? ""} maxLength={200} />
      <SelectField
        label="Verdict"
        value={filters.verdict ?? ANY}
        options={[{ value: ANY, label: "Any verdict" }, ...VERDICT_OPTIONS]}
        onChange={(event) => onChange({ ...filters, ...typed(event.target.form), verdict: VERDICT_OPTIONS.find((o) => o.value === event.target.value)?.value })}
      />
      <SelectField
        label="Status"
        value={filters.status ?? ANY}
        options={[{ value: ANY, label: "Any status" }, ...STATUS_OPTIONS]}
        onChange={(event) => onChange({ ...filters, ...typed(event.target.form), status: STATUS_OPTIONS.find((o) => o.value === event.target.value)?.value })}
      />
      <SelectField
        label="Started"
        value={filters.range ?? ANY}
        options={[{ value: ANY, label: "Any time" }, ...RANGE_OPTIONS]}
        onChange={(event) => onChange({ ...filters, ...typed(event.target.form), range: event.target.value || undefined })}
      />
      <TextField label="Route" name="route" defaultValue={filters.route ?? ""} />
      <TextField label="Service" name="service" defaultValue={filters.service ?? ""} />
      <TextField label="Model" name="model" defaultValue={filters.model ?? ""} />
      <div className="sk-filters__actions">
        <Button type="submit" variant="primary">
          Apply
        </Button>
        <Button onClick={() => onChange({})}>Clear</Button>
      </div>
    </form>
  );
}
