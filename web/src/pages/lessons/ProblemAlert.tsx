import { CODE_STALE, SaveError } from "../../api/rules";
import { Alert } from "../../components/Alert";

/** Turns anything a failed save threw into a SaveError, so the page can say why. */
export function saveErrorOf(e: Error): SaveError {
  return e instanceof SaveError ? e : new SaveError(0, e.message, undefined, []);
}

/**
 * Says why a change was refused. A change to something that changed meanwhile
 * has been read again by the page.
 */
export function ProblemAlert({ error, noun }: { error: SaveError; noun: string }) {
  if (error.code === CODE_STALE) {
    return <Alert tone="warning" title={`Someone changed this ${noun}`} />;
  }

  return (
    <Alert tone="danger" title={`Could not save the ${noun}`}>
      <p>{error.message}</p>
    </Alert>
  );
}
