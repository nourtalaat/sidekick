import { useState, type FormEvent } from "react";
import type { LessonFilters, LessonStatus } from "../../api/lessons";
import { Button } from "../../components/Button";
import { Select } from "../../components/Select";
import { TextField } from "../../components/TextField";
import "./LessonsPage.css";

export const EMPTY_LESSON_FILTERS: LessonFilters = { status: "", service: "", q: "" };

const STATUS_OPTIONS: readonly { value: LessonStatus | ""; label: string }[] = [
  { value: "", label: "Every status" },
  { value: "active", label: "Active" },
  { value: "disabled", label: "Disabled" },
  { value: "proposed", label: "Waits for review" },
  { value: "rejected", label: "Rejected" },
];

/** The filter fields of the lessons list. They start from the address and change it only when the form is applied. */
export function LessonFilterForm({ filters, onApply }: { filters: LessonFilters; onApply(next: LessonFilters): void }) {
  const [draft, setDraft] = useState(filters);
  const set = (patch: Partial<LessonFilters>) => setDraft((d) => ({ ...d, ...patch }));

  function submit(event: FormEvent) {
    event.preventDefault();
    onApply(draft);
  }

  return (
    <form className="sk-filters" onSubmit={submit} aria-label="Filter the lessons">
      <Select label="Status" value={draft.status} onChange={(e) => set({ status: e.target.value as LessonFilters["status"] })} options={STATUS_OPTIONS} />
      <TextField label="Service" value={draft.service} onChange={(e) => set({ service: e.target.value })} autoComplete="off" help="Lessons scoped to this service." />
      <TextField label="Words" value={draft.q} onChange={(e) => set({ q: e.target.value })} autoComplete="off" maxLength={200} help="Words in the text of the lesson." />
      <div className="sk-filters__buttons">
        <Button type="submit" variant="primary">
          Filter
        </Button>
        <Button
          onClick={() => {
            setDraft(EMPTY_LESSON_FILTERS);
            onApply(EMPTY_LESSON_FILTERS);
          }}
        >
          Clear filters
        </Button>
      </div>
    </form>
  );
}
