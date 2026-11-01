import { useEffect, useRef, useState, type FormEvent, type Ref } from "react";
import { CODE_STALE, CODE_VALIDATION, SaveError } from "../../api/rules";
import { CODE_UNCHANGED, useSaveStrategyVersion } from "../../api/strategies";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { TextArea } from "../../components/TextArea";
import { TextField } from "../../components/TextField";
import { DiffView } from "./DiffView";
import { saveProblems } from "./problems";
import "./StrategyEditor.css";

const EDITOR_ROWS = 24;

interface StrategyEditorProps {
  name: string;
  /** The YAML the editor starts with, and the text Reset goes back to. */
  initialBody: string;
  /** The version that was read, or 0 for a strategy that is not stored yet. The save is refused if someone saved since. */
  baseVersion: number;
  /** Says where the text comes from when it is not the current version. */
  note?: string;
  /** Loads the strategy again after someone else saved; the editor starts over from it. */
  onReload: () => void;
  /** Called with null when a save starts and with the number of the version when it succeeds. */
  onSaved: (version: number | null) => void;
}

/**
 * The YAML of a runtime strategy in a text box, with a required reason. The
 * server checks the YAML and answers with the path and line of each problem,
 * which the editor lists above the text.
 */
export function StrategyEditor({ name, initialBody, baseVersion, note, onReload, onSaved }: StrategyEditorProps) {
  const save = useSaveStrategyVersion(name);
  const alertRef = useRef<HTMLDivElement>(null);

  const [body, setBody] = useState(initialBody);
  const [reason, setReason] = useState("");
  const [savedBody, setSavedBody] = useState(initialBody);
  const [showChanges, setShowChanges] = useState(false);
  const [failures, setFailures] = useState(0);

  const error = save.error instanceof SaveError ? save.error : null;
  const problems = error?.code === CODE_VALIDATION ? saveProblems(error.fields) : null;
  const changed = body !== savedBody;

  // A refused save moves focus to the message, so a screen reader reads it and the page shows it.
  useEffect(() => {
    if (failures > 0) {
      alertRef.current?.focus();
    }
  }, [failures]);

  function submit(event: FormEvent) {
    event.preventDefault();
    onSaved(null);

    save.mutate(
      { body, reason, baseVersion },
      {
        onSuccess: (saved) => {
          setSavedBody(body);
          onSaved(saved.version);
          setReason("");
          setShowChanges(false);
        },
        onError: () => setFailures((n) => n + 1),
      },
    );
  }

  return (
    <form className="sk-strategy-editor" onSubmit={submit} noValidate>
      {error && <SaveMessage ref={alertRef} error={error} problems={problems?.body ?? []} onReload={onReload} />}
      {note && <p>{note}</p>}
      <TextArea
        label="Strategy (YAML)"
        help="The format of a strategy file. The name must stay the same."
        mono
        rows={EDITOR_ROWS}
        spellCheck={false}
        value={body}
        onChange={(event) => setBody(event.target.value)}
      />
      <TextField
        label="Reason for this version"
        help="Required. It is kept with the version, in the history of the strategy."
        value={reason}
        error={problems?.reason}
        onChange={(event) => setReason(event.target.value)}
      />
      {showChanges && <DiffView before={savedBody} after={body} label="Your changes" />}
      <div className="sk-strategy-editor__buttons">
        <Button type="submit" variant="primary" disabled={save.isPending}>
          {save.isPending ? "Saving" : "Save as a new version"}
        </Button>
        <Button disabled={!changed} onClick={() => setShowChanges((shown) => !shown)}>
          {showChanges ? "Hide my changes" : "Show my changes"}
        </Button>
        <Button
          disabled={!changed}
          onClick={() => {
            setBody(savedBody);
            setShowChanges(false);
          }}
        >
          Reset
        </Button>
      </div>
    </form>
  );
}

interface SaveMessageProps {
  error: SaveError;
  problems: readonly string[];
  onReload: () => void;
  ref: Ref<HTMLDivElement>;
}

/** What went wrong with a save, in words, and what to do about it. */
function SaveMessage({ error, problems, onReload, ref }: SaveMessageProps) {
  if (error.code === CODE_STALE) {
    return (
      <Alert ref={ref} tone="warning" title="Someone saved a newer version">
        <p>Your text was not saved. Load the newest version to see what changed; your edits on this page are lost.</p>
        <Button onClick={onReload}>Load the newest version</Button>
      </Alert>
    );
  }

  if (error.code === CODE_UNCHANGED) {
    return (
      <Alert ref={ref} tone="warning" title="Nothing to save">
        <p>The text is the same as the current version. Change it first.</p>
      </Alert>
    );
  }

  if (error.code === CODE_VALIDATION) {
    return (
      <Alert ref={ref} tone="danger" title={problems.length > 0 ? "Not saved. The strategy has problems" : "Not saved. Fix the field marked below"}>
        {problems.length > 0 && (
          <ul className="sk-strategy-editor__problems">
            {problems.map((problem) => (
              <li key={problem}>{problem}</li>
            ))}
          </ul>
        )}
      </Alert>
    );
  }

  return (
    <Alert ref={ref} tone="danger" title="Could not save">
      <p>{error.message}</p>
    </Alert>
  );
}
