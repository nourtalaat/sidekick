import { useEffect, useRef } from "react";
import { Button } from "../../components/Button";
import "./OverrideConfirm.css";

interface OverrideConfirmProps {
  /** The name of the route that is being saved. */
  route: string;
  busy?: boolean;
  onConfirm(): void;
  onCancel(): void;
}

/**
 * The confirm step of saving a read-write route that may select senders
 * that are not trusted. It sits in the page, as the confirm step of a delete
 * does, and focus starts on Cancel, so a stray Enter allows nothing.
 */
export function OverrideConfirm({ route, busy = false, onConfirm, onCancel }: OverrideConfirmProps) {
  const cancel = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    cancel.current?.focus();
  }, []);

  return (
    <div role="group" aria-label="Confirm writes for senders that are not trusted" className="sk-override-confirm">
      <p className="sk-override-confirm__question">Run actions without approval for senders that are not trusted?</p>
      <div className="sk-override-confirm__text">
        <p>
          Route {route} will approve and run the model&apos;s actions by itself, also for events from senders that nobody vouches for. The text of such an event can steer the model.
        </p>
        <p>Every start of Sidekick logs a warning for this route, and the change goes in the audit log.</p>
      </div>
      <div className="sk-override-confirm__actions">
        <Button variant="destructive" disabled={busy} onClick={onConfirm}>
          Allow untrusted writes
        </Button>
        <Button ref={cancel} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}
