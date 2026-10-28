import { TriangleAlert } from "lucide-react";
import { ApiError } from "../api/client";
import { Button } from "./Button";
import "./QueryError.css";

const ROLE_IN_DETAIL = /needs the (\w+) role/i;
const FORBIDDEN = 403;

/**
 * Says why a request failed. An answer of 403 names the role the page needs;
 * any other failure offers a retry when onRetry is given.
 */
export function QueryError({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const status = error instanceof ApiError ? error.status : undefined;
  const detail = error instanceof Error ? error.message : "";

  if (status === FORBIDDEN) {
    const role = ROLE_IN_DETAIL.exec(detail)?.[1]?.toLowerCase();

    return (
      <div role="alert" className="sk-query-error">
        <TriangleAlert size={20} aria-hidden="true" />
        <div>
          <h2 className="sk-query-error__title">{role ? `Needs the ${role} role` : "Needs a higher role"}</h2>
          <p className="sk-query-error__text">Your role does not allow this. Roles come from your groups at the identity provider.</p>
        </div>
      </div>
    );
  }

  return (
    <div role="alert" className="sk-query-error">
      <TriangleAlert size={20} aria-hidden="true" />
      <div>
        <h2 className="sk-query-error__title">Could not load this page</h2>
        {detail && <p className="sk-query-error__text">{detail}</p>}
        {onRetry && <Button onClick={onRetry}>Try again</Button>}
      </div>
    </div>
  );
}
