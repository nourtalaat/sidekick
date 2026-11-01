import { useState, type FormEvent } from "react";
import { MAX_NOTES_CHARS, useSaveServiceNotes, useServiceNotes, type ServiceNotes } from "../../api/notes";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { DeleteConfirm } from "../../components/DeleteConfirm";
import { QueryError } from "../../components/QueryError";
import { TextArea } from "../../components/TextArea";
import { formatTime } from "../rules/format";
import { saveErrorOf } from "./ProblemAlert";
import "./LessonsPage.css";

interface NotesEditorProps {
  service: string;
  /** Whether the signed-in user may change the notes. */
  canChange: boolean;
  /** Called with a sentence for the page to show once the notes changed. */
  onChanged(message: string): void;
  /** Called when a change starts, so the page can take the last sentence away. */
  onChanging(): void;
}

/** The notes about one service: who wrote them last and when, and for an admin a plain-text editor. */
export function NotesEditor({ service, canChange, onChanged, onChanging }: NotesEditorProps) {
  const notes = useServiceNotes(service);

  return (
    <section className="sk-lesson" aria-label={`Notes about ${service}`}>
      <h2 className="sk-manage__heading">Notes about {service}</h2>
      {notes.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {notes.isError && <QueryError error={notes.error} onRetry={() => void notes.refetch()} />}
      {notes.isSuccess && <NotesBody notes={notes.data} canChange={canChange} onChanged={onChanged} onChanging={onChanging} />}
    </section>
  );
}

/** Who changed the notes last and when, or a line that says there are none yet. */
function LastChange({ notes }: { notes: ServiceNotes }) {
  if (!notes.updatedAt) {
    return <p className="sk-manage__note">Nobody has written notes about this service yet.</p>;
  }

  return (
    <p className="sk-manage__note">
      Last changed {notes.updatedByName ? `by ${notes.updatedByName} ` : "by an API token "}on <time dateTime={notes.updatedAt}>{formatTime(notes.updatedAt)}</time>.
    </p>
  );
}

function NotesBody({ notes, canChange, onChanged, onChanging }: { notes: ServiceNotes } & Omit<NotesEditorProps, "service">) {
  return (
    <>
      <LastChange notes={notes} />
      {canChange ? (
        <NotesForm key={`${notes.service}:${notes.updatedAt ?? ""}`} notes={notes} onChanged={onChanged} onChanging={onChanging} />
      ) : (
        notes.text !== "" && <p className="sk-manage__text">{notes.text}</p>
      )}
    </>
  );
}

function NotesForm({ notes, onChanged, onChanging }: { notes: ServiceNotes } & Pick<NotesEditorProps, "onChanged" | "onChanging">) {
  const save = useSaveServiceNotes();

  const [text, setText] = useState(notes.text);
  const [confirming, setConfirming] = useState(false);

  const error = save.error ? saveErrorOf(save.error) : null;
  const deleting = text.trim() === "" && notes.text !== "";
  const length = [...text].length;

  function submit(event: FormEvent) {
    event.preventDefault();

    if (deleting) {
      onChanging();
      setConfirming(true);

      return;
    }

    write(text.trim(), "Notes saved. The memory source shows them to the model.");
  }

  function write(next: string, message: string) {
    onChanging();
    save.mutate({ service: notes.service, text: next }, { onSuccess: () => onChanged(message), onSettled: () => setConfirming(false) });
  }

  return (
    <form className="sk-manage__form" onSubmit={submit} aria-label={`Edit the notes about ${notes.service}`}>
      {error && (
        <Alert tone="danger" title="Could not save the notes">
          {(error.fields.length > 0 ? error.fields.map((f) => f.message) : [error.message]).map((message) => (
            <p key={message}>{message}</p>
          ))}
        </Alert>
      )}
      <TextArea
        label="Notes"
        rows={8}
        value={text}
        onChange={(e) => setText(e.target.value)}
        help={`${length} of ${MAX_NOTES_CHARS} characters. Plain text. Secrets are removed when you save. Saving empty text deletes the notes.`}
      />
      {confirming ? (
        <DeleteConfirm subject="notes" determiner="these" busy={save.isPending} onConfirm={() => write("", "Notes deleted.")} onCancel={() => setConfirming(false)}>
          <p>The notes about {notes.service} are removed, and the memory source finds no notes for it. The audit log keeps their text.</p>
        </DeleteConfirm>
      ) : (
        <div className="sk-lesson__actions">
          <Button type="submit" variant={deleting ? "destructive" : "primary"} disabled={save.isPending}>
            {deleting ? "Delete notes" : "Save notes"}
          </Button>
        </div>
      )}
    </form>
  );
}
