import type { UseQueryResult } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { QueryError } from "../../components/QueryError";

/**
 * Shows what a query holds: a polite loading line while it loads, the error
 * with a retry when it failed, and children with the data once it is there.
 */
export function Loaded<T>({ result, children }: { result: UseQueryResult<T>; children: (data: T) => ReactNode }) {
  if (result.isPending) {
    return (
      <p role="status" className="sk-loading">
        Loading
      </p>
    );
  }

  if (result.isError) {
    return <QueryError error={result.error} onRetry={() => void result.refetch()} />;
  }

  return <>{children(result.data)}</>;
}
