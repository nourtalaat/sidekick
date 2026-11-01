import { useEffect, useRef, type ReactNode } from "react";
import { Button } from "./Button";
import "./DeleteConfirm.css";

interface DeleteConfirmProps {
  /** What is deleted, such as "lesson", for the question and the button. */
  subject: string;
  /** What deleting does, when the person needs to know before they decide. */
  children?: ReactNode;
  busy?: boolean;
  onConfirm(): void;
  onCancel(): void;
}

/**
 * The confirm step of a delete, shown in the page where the delete button
 * was, instead of a browser dialog. Focus starts on Cancel, so a stray Enter
 * deletes nothing.
 */
export function DeleteConfirm({ subject, children, busy = false, onConfirm, onCancel }: DeleteConfirmProps) {
  const cancel = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    cancel.current?.focus();
  }, []);

  return (
    <div role="group" aria-label={`Delete this ${subject}`} className="sk-delete-confirm">
      <p className="sk-delete-confirm__question">Delete this {subject}?</p>
      {children && <div className="sk-delete-confirm__text">{children}</div>}
      <div className="sk-delete-confirm__actions">
        <Button variant="destructive" disabled={busy} onClick={onConfirm}>
          Delete {subject}
        </Button>
        <Button ref={cancel} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}
