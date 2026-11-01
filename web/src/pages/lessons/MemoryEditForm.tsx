import { useState, type FormEvent } from "react";
import { MAX_MEMORY_CHARS, useUpdateMemoryEntry, type MemoryEntry } from "../../api/memory";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { TextArea } from "../../components/TextArea";
import { sortErrors } from "../rules/formHelpers";
import { saveErrorOf } from "./ProblemAlert";
import "../review/LessonsTab.css";
import "./LessonsPage.css";

const FIELDS = ["summary", "cause"] as const;

interface MemoryEditFormProps {
  entry: MemoryEntry;
  onDone(): void;
  onSaved(): void;
  onChanging(): void;
}

/** The form that edits the summary and cause of a memory entry. It says how long the edit lasts. */
export function MemoryEditForm({ entry, onDone, onSaved, onChanging }: MemoryEditFormProps) {
  const update = useUpdateMemoryEntry();

  const [summary, setSummary] = useState(entry.summary);
  const [cause, setCause] = useState(entry.cause ?? "");

  const error = update.error ? saveErrorOf(update.error) : null;
  const errors = sortErrors(error?.fields ?? [], FIELDS);

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    onChanging();

    update.mutate(
      { id: entry.investigationId, body: { summary: summary.trim(), cause: cause.trim() || undefined, disabled: entry.disabled } },
      {
        onSuccess: () => {
          onSaved();
          onDone();
        },
      },
    );
  }

  return (
    <form className="sk-lesson sk-lesson--editing" onSubmit={onSubmit} aria-label="Edit memory entry">
      {error && (errors.other.length > 0 || Object.keys(errors.byField).length === 0) && (
        <Alert tone="danger" title="Could not save the entry">
          {errors.other.length > 0 ? errors.other.map((message) => <p key={message}>{message}</p>) : <p>{error.message}</p>}
        </Alert>
      )}
      <p className="sk-manage__note">
        Your edit lasts until the confirmation of this investigation changes, for example when a responder rates its verdict again. Then Sidekick writes the
        entry again from the investigation, and the text you wrote is replaced. The entry stays disabled if it is disabled. To keep an entry out of prompts for
        good, disable it.
      </p>
      <TextArea
        label="Summary"
        rows={4}
        value={summary}
        onChange={(e) => setSummary(e.target.value)}
        help={`${[...summary].length} of ${MAX_MEMORY_CHARS} characters`}
        error={errors.byField["summary"]}
        required
      />
      <TextArea
        label="Cause"
        rows={3}
        value={cause}
        onChange={(e) => setCause(e.target.value)}
        help={`${[...cause].length} of ${MAX_MEMORY_CHARS} characters. Leave it empty for an entry without a cause.`}
        error={errors.byField["cause"]}
      />
      <div className="sk-lesson__actions">
        <Button type="submit" variant="primary" disabled={update.isPending}>
          Save entry
        </Button>
        <Button onClick={onDone}>Cancel</Button>
      </div>
    </form>
  );
}
