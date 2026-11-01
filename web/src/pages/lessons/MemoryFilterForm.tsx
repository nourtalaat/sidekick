import { useState, type FormEvent } from "react";
import type { DisabledFilter, MemoryFilters, MemoryVerdict } from "../../api/memory";
import { Button } from "../../components/Button";
import { Select } from "../../components/Select";
import { TextField } from "../../components/TextField";
import "./LessonsPage.css";

export const EMPTY_MEMORY_FILTERS: MemoryFilters = { service: "", verdict: "", q: "", disabled: "" };

/** The verdicts as people read them, in the order the dashboard lists them. */
export const VERDICT_LABELS: Record<MemoryVerdict, string> = {
  true_positive: "Real issue",
  false_positive: "False alarm",
  inconclusive: "Not sure yet",
  duplicate: "Duplicate",
  known_issue: "Known issue",
  flapping: "Flapping",
};

const VERDICT_OPTIONS: readonly { value: MemoryVerdict | ""; label: string }[] = [
  { value: "", label: "Every verdict" },
  ...(Object.entries(VERDICT_LABELS) as [MemoryVerdict, string][]).map(([value, label]) => ({ value, label })),
];

const DISABLED_OPTIONS: readonly { value: DisabledFilter; label: string }[] = [
  { value: "", label: "Enabled and disabled" },
  { value: "enabled", label: "Enabled only" },
  { value: "disabled", label: "Disabled only" },
];

/** The filter fields of the memory list. They start from the address and change it only when the form is applied. */
export function MemoryFilterForm({ filters, onApply }: { filters: MemoryFilters; onApply(next: MemoryFilters): void }) {
  const [draft, setDraft] = useState(filters);
  const set = (patch: Partial<MemoryFilters>) => setDraft((d) => ({ ...d, ...patch }));

  function submit(event: FormEvent) {
    event.preventDefault();
    onApply(draft);
  }

  return (
    <form className="sk-filters" onSubmit={submit} aria-label="Filter the memory entries">
      <TextField label="Service" value={draft.service} onChange={(e) => set({ service: e.target.value })} autoComplete="off" />
      <Select label="Verdict" value={draft.verdict} onChange={(e) => set({ verdict: e.target.value as MemoryFilters["verdict"] })} options={VERDICT_OPTIONS} />
      <TextField
        label="Words"
        value={draft.q}
        onChange={(e) => set({ q: e.target.value })}
        autoComplete="off"
        maxLength={200}
        help="Words in the service, alert name, summary or cause."
      />
      <Select label="State" value={draft.disabled} onChange={(e) => set({ disabled: e.target.value as DisabledFilter })} options={DISABLED_OPTIONS} />
      <div className="sk-filters__buttons">
        <Button type="submit" variant="primary">
          Filter
        </Button>
        <Button
          onClick={() => {
            setDraft(EMPTY_MEMORY_FILTERS);
            onApply(EMPTY_MEMORY_FILTERS);
          }}
        >
          Clear filters
        </Button>
      </div>
    </form>
  );
}
