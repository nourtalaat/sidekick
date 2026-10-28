import { useEffect, useRef, type FormEvent, type ReactNode, type Ref } from "react";
import { Alert } from "../../components/Alert";
import { Button, LinkButton } from "../../components/Button";
import { CODE_EXISTS, CODE_FILE_RULE, CODE_STALE, CODE_VALIDATION, SaveError } from "../../api/rules";
import "./RuleForm.css";

interface RuleFormFrameProps {
  title: string;
  /** Where Cancel goes. */
  listPath: string;
  submitLabel: string;
  saving: boolean;
  /** The refused save, if any. */
  error: SaveError | null;
  /** Problems the form found itself, or the server found in fields it shows. */
  invalid: boolean;
  /** Counts the refused saves, so focus moves to the message after each one, also when the message is the same. */
  failures: number;
  /** Problems with no field of the form to sit under. */
  otherProblems: readonly string[];
  /** Loads the rule again after someone else changed it; the form starts over from it. */
  onReload: () => void;
  onSubmit: () => void;
  children: ReactNode;
}

/** The frame of a listener or route form: its title, the message about a refused save, and the buttons. */
export function RuleFormFrame({ title, listPath, submitLabel, saving, error, invalid, failures, otherProblems, onReload, onSubmit, children }: RuleFormFrameProps) {
  const alertRef = useRef<HTMLDivElement>(null);

  // A refused save moves focus to the message, so a screen reader reads it and the page shows it.
  useEffect(() => {
    if (failures > 0) {
      alertRef.current?.focus();
    }
  }, [failures]);

  function submit(event: FormEvent) {
    event.preventDefault();
    onSubmit();
  }

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">{title}</h1>
      {(error || invalid) && <SaveMessage ref={alertRef} error={error} invalid={invalid} otherProblems={otherProblems} onReload={onReload} />}
      <form className="sk-rule-form" onSubmit={submit} noValidate>
        {children}
        <div className="sk-rule-form__buttons">
          <Button type="submit" variant="primary" disabled={saving}>
            {saving ? "Saving" : submitLabel}
          </Button>
          <LinkButton to={listPath}>Cancel</LinkButton>
        </div>
      </form>
    </div>
  );
}

interface SaveMessageProps {
  error: SaveError | null;
  invalid: boolean;
  otherProblems: readonly string[];
  onReload: () => void;
  ref: Ref<HTMLDivElement>;
}

/** What went wrong with a save, in words, and what to do about it. */
function SaveMessage({ error, invalid, otherProblems, onReload, ref }: SaveMessageProps) {
  if (error?.code === CODE_STALE) {
    return (
      <Alert ref={ref} tone="warning" title="Someone changed this since you opened it">
        <p>Your changes were not saved. Load the new version to see what changed; your edits on this page are lost.</p>
        <Button onClick={onReload}>Load the new version</Button>
      </Alert>
    );
  }

  if (error && error.code !== CODE_VALIDATION) {
    const title = error.code === CODE_FILE_RULE ? "This is set in the config file" : error.code === CODE_EXISTS ? "That name is taken" : "Could not save";

    return (
      <Alert ref={ref} tone="danger" title={title}>
        <p>{error.message}</p>
      </Alert>
    );
  }

  if (!invalid) {
    return null;
  }

  return (
    <Alert ref={ref} tone="danger" title="Not saved. Fix the fields marked below">
      {otherProblems.length > 0 && (
        <ul>
          {otherProblems.map((problem) => (
            <li key={problem}>{problem}</li>
          ))}
        </ul>
      )}
    </Alert>
  );
}
