import { useEffect, useRef } from "react";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { CopyButton } from "../../components/CopyButton";
import "./TokensTab.css";

/**
 * Shows the secret of a token that was just made, the only time it is shown.
 * It takes focus when it appears, so a screen reader reads it. The secret is
 * plain text that a person can select.
 */
export function NewTokenAlert({ name, secret, onDone }: { name: string; secret: string; onDone(): void }) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    ref.current?.focus();
  }, []);

  return (
    <Alert ref={ref} tone="success" title="Token created">
      <p>
        The token <strong>{name}</strong> is ready. Copy it now. It is not shown again.
      </p>
      <code className="sk-tokens__secret">{secret}</code>
      <div className="sk-tokens__secret-actions">
        <CopyButton text={secret} label="Copy" />
        <Button onClick={onDone}>Done</Button>
      </div>
    </Alert>
  );
}
