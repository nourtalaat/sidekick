import { Link, useParams, useSearchParams } from "react-router";
import { useStrategyVersion, type StrategyVersionDetail } from "../api/strategies";
import { EmptyState } from "../components/EmptyState";
import { QueryError } from "../components/QueryError";
import { DiffView } from "./strategies/DiffView";
import { formatTime } from "./rules/format";

/** The changes between two saved versions of a runtime strategy, named by the from and to query parameters. */
export function StrategyDiffPage() {
  const { name = "" } = useParams();
  const [search] = useSearchParams();
  const from = positive(search.get("from"));
  const to = positive(search.get("to"));
  const before = useStrategyVersion(name, from);
  const after = useStrategyVersion(name, to);
  const back = `/strategies/${encodeURIComponent(name)}`;

  if (from === undefined || to === undefined) {
    return (
      <div className="sk-page">
        <h1 className="sk-page__title">Changes in {name}</h1>
        <EmptyState title="Pick two versions">
          <Link to={back}>Choose them in the history of {name}</Link>
        </EmptyState>
      </div>
    );
  }

  const failed = before.isError ? before : after.isError ? after : null;

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">
        Changes in {name}, version {from} to version {to}
      </h1>
      <p>
        <Link to={back}>Back to {name}</Link>
      </p>
      {failed && <QueryError error={failed.error} onRetry={() => void failed.refetch()} />}
      {!failed && (before.isPending || after.isPending) && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {before.data && after.data && (
        <>
          <ul>
            <li>{describe(before.data)}</li>
            <li>{describe(after.data)}</li>
          </ul>
          <DiffView before={before.data.body} after={after.data.body} label={`Changes from version ${from} to version ${to}`} />
        </>
      )}
    </div>
  );
}

/** The version number in a query parameter, or undefined when it is not a whole number above zero. */
function positive(value: string | null): number | undefined {
  const n = Number(value);

  return value !== null && Number.isInteger(n) && n > 0 ? n : undefined;
}

/** One line about a version: who saved it, when and why. */
function describe(v: StrategyVersionDetail): string {
  return `Version ${v.version}, saved ${formatTime(v.createdAt)}${v.authorName ? ` by ${v.authorName}` : ""}: ${v.reason}`;
}
