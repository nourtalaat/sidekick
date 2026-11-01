import { useId, useState } from "react";
import { Link } from "react-router";
import { useDeleteMemoryEntry, useUpdateMemoryEntry, type MemoryEntry } from "../../api/memory";
import type { SaveError } from "../../api/rules";
import { StatusBadge, VerdictBadge } from "../../components/Badge";
import { Button } from "../../components/Button";
import { DeleteConfirm } from "../../components/DeleteConfirm";
import { formatTime } from "../rules/format";
import { MemoryEditForm } from "./MemoryEditForm";
import { ProblemAlert, saveErrorOf } from "./ProblemAlert";
import "../review/LessonsTab.css";
import "./LessonsPage.css";

interface MemoryEntryRowProps {
  entry: MemoryEntry;
  /** Whether the signed-in user may edit, disable, enable and delete. */
  canChange: boolean;
  /** Called with a sentence for the page to show once an entry changed. */
  onChanged(message: string): void;
  /** Called when a change starts, so the page can take the last sentence away. */
  onChanging(): void;
}

/** One memory entry: the verdict people confirmed, its summary and cause, and the changes an admin can make. */
export function MemoryEntryRow({ entry, canChange, onChanged, onChanging }: MemoryEntryRowProps) {
  const summaryId = useId();
  const update = useUpdateMemoryEntry();
  const remove = useDeleteMemoryEntry();

  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<SaveError | null>(null);

  const busy = update.isPending || remove.isPending;
  const label = entry.alertName ?? entry.service ?? entry.investigationId.slice(-8);

  function begin() {
    onChanging();
    setError(null);
  }

  function setDisabled(disabled: boolean) {
    begin();
    update.mutate(
      { id: entry.investigationId, body: { summary: entry.summary, cause: entry.cause, disabled } },
      {
        onSuccess: () => onChanged(disabled ? "Entry disabled. Prompts never show it and searches never find it." : "Entry enabled. Prompts can show it again."),
        onError: (e) => setError(saveErrorOf(e)),
      },
    );
  }

  function onDelete() {
    begin();
    remove.mutate(entry.investigationId, {
      onSuccess: () => onChanged("Entry deleted."),
      onError: (e) => {
        setConfirming(false);
        setError(saveErrorOf(e));
      },
    });
  }

  if (editing) {
    return (
      <MemoryEditForm
        entry={entry}
        onDone={() => setEditing(false)}
        onSaved={() => onChanged("Entry saved. It stays as you wrote it until the confirmation of its investigation changes.")}
        onChanging={onChanging}
      />
    );
  }

  return (
    <article className="sk-lesson" aria-labelledby={summaryId}>
      <div className="sk-manage__badges">
        <VerdictBadge verdict={entry.verdict} />
        {entry.disabled && <StatusBadge status="neutral">Disabled</StatusBadge>}
      </div>
      <p id={summaryId} className="sk-lesson__text">
        {entry.summary}
      </p>
      {entry.cause && (
        <p className="sk-manage__secondary">
          <strong>Cause:</strong> {entry.cause}
        </p>
      )}
      <dl className="sk-lesson__facts">
        {entry.service && (
          <div>
            <dt>Service</dt>
            <dd>{entry.service}</dd>
          </div>
        )}
        {entry.alertName && (
          <div>
            <dt>Alert</dt>
            <dd>{entry.alertName}</dd>
          </div>
        )}
        <div>
          <dt>Confirmed</dt>
          <dd>
            <time dateTime={entry.confirmedAt}>{formatTime(entry.confirmedAt)}</time>
          </dd>
        </div>
        <div>
          <dt>Investigation</dt>
          <dd>
            <Link to={`/investigations/${entry.investigationId}`}>Open investigation {entry.investigationId.slice(-8)}</Link>
          </dd>
        </div>
      </dl>
      {error && <ProblemAlert error={error} noun="entry" />}
      {canChange && !confirming && (
        <div className="sk-lesson__actions">
          <Button
            disabled={busy}
            onClick={() => {
              begin();
              setEditing(true);
            }}
            aria-label={`Edit memory entry: ${label}`}
          >
            Edit
          </Button>
          {entry.disabled ? (
            <Button disabled={busy} onClick={() => setDisabled(false)} aria-label={`Enable memory entry: ${label}`}>
              Enable
            </Button>
          ) : (
            <Button disabled={busy} onClick={() => setDisabled(true)} aria-label={`Disable memory entry: ${label}`}>
              Disable
            </Button>
          )}
          <Button
            disabled={busy}
            onClick={() => {
              begin();
              setConfirming(true);
            }}
            aria-label={`Delete memory entry: ${label}`}
          >
            Delete
          </Button>
        </div>
      )}
      {canChange && confirming && (
        <DeleteConfirm subject="memory entry" busy={remove.isPending} onConfirm={onDelete} onCancel={() => setConfirming(false)}>
          <p>
            The entry and its embedding are removed. If the confirmation of its investigation changes later, Sidekick writes a new entry. To keep an entry out of
            prompts for good, disable it instead.
          </p>
        </DeleteConfirm>
      )}
    </article>
  );
}
