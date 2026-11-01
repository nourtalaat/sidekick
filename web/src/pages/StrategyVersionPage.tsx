import { Link, useParams } from "react-router";
import { ApiError } from "../api/client";
import { useStrategyVersion } from "../api/strategies";
import { EmptyState } from "../components/EmptyState";
import { LinkButton } from "../components/Button";
import { QueryError } from "../components/QueryError";
import { hasRole } from "../roles";
import { useCurrentUser } from "../session";
import { formatTime } from "./rules/format";
import { YamlText } from "./strategies/YamlText";
import "./strategies/Page.css";

const NOT_FOUND = 404;

/** One saved version of a runtime strategy: who saved it, why, and its text. */
export function StrategyVersionPage() {
  const { name = "", version = "" } = useParams();
  const me = useCurrentUser();
  const number = Number.parseInt(version, 10);
  const saved = useStrategyVersion(name, Number.isInteger(number) && number > 0 ? number : undefined);
  const base = `/strategies/${encodeURIComponent(name)}`;

  if (!Number.isInteger(number) || number < 1) {
    return <NoVersion name={name} />;
  }

  if (saved.isPending) {
    return (
      <p role="status" className="sk-loading">
        Loading
      </p>
    );
  }

  if (saved.isError) {
    return saved.error instanceof ApiError && saved.error.status === NOT_FOUND ? (
      <NoVersion name={name} />
    ) : (
      <QueryError error={saved.error} onRetry={() => void saved.refetch()} />
    );
  }

  const v = saved.data;

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">
        {name}, version {v.version}
      </h1>
      <p>
        Saved <time dateTime={v.createdAt}>{formatTime(v.createdAt)}</time>
        {v.authorName ? ` by ${v.authorName}` : ""}. {v.current ? "This is the current version." : "A newer version exists."}
      </p>
      <p>Reason: {v.reason}</p>
      <YamlText text={v.body} />
      <div className="sk-page__actions">
        {v.version > 1 && <LinkButton to={`${base}/diff?from=${v.version - 1}&to=${v.version}`}>Show what this version changed</LinkButton>}
        {hasRole(me.role, "admin") && !v.current && <LinkButton to={`${base}?base=${v.version}`}>Use this version as the base</LinkButton>}
        <LinkButton to={base}>Back to {name}</LinkButton>
      </div>
    </div>
  );
}

function NoVersion({ name }: { name: string }) {
  return (
    <div className="sk-page">
      <h1 className="sk-page__title">No such version</h1>
      <EmptyState title="That version does not exist">
        <Link to={`/strategies/${encodeURIComponent(name)}`}>Back to {name}</Link>
      </EmptyState>
    </div>
  );
}
