import { Link } from "react-router";
import { useStrategies, type Strategy } from "../api/strategies";
import { EmptyState } from "../components/EmptyState";
import { QueryError } from "../components/QueryError";
import { Table } from "../components/Table";
import { formatTime } from "./rules/format";
import { StrategyLayerBadge, StrategyStatusBadge } from "./strategies/badges";

/** The built-in strategies, those of the strategies directory and those added at runtime, with the layer and version of each. */
export function StrategiesPage() {
  const strategies = useStrategies();

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Strategies</h1>
      <p>
        A strategy tells an investigation what to check first for one kind of event. A strategy added at runtime replaces a built-in strategy of the same name,
        and a strategy file replaces both. Built-in strategies and strategy files cannot be changed here. Every save of a runtime strategy is a new version,
        and an investigation keeps the version it started with.
      </p>
      {strategies.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {strategies.isError && <QueryError error={strategies.error} onRetry={() => void strategies.refetch()} />}
      {strategies.isSuccess && strategies.data.length === 0 && <EmptyState title="No strategies">Every deployment has the built-in strategies.</EmptyState>}
      {strategies.isSuccess && strategies.data.length > 0 && (
        <Table label="Strategies">
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Status</th>
              <th scope="col">Where</th>
              <th scope="col">Version</th>
              <th scope="col">Description</th>
              <th scope="col">Changed</th>
            </tr>
          </thead>
          <tbody>
            {strategies.data.map((s) => (
              <StrategyRow key={`${s.layer}/${s.name}`} strategy={s} />
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}

function StrategyRow({ strategy: s }: { strategy: Strategy }) {
  return (
    <tr>
      <th scope="row">
        <Link to={`/strategies/${encodeURIComponent(s.name)}`}>{s.name}</Link>
      </th>
      <td>
        <StrategyStatusBadge status={s.status} reason={s.statusReason} />
      </td>
      <td>
        <StrategyLayerBadge layer={s.layer} />
        {s.overrides && s.overrides.length > 0 && <span className="sk-table__secondary">Replaces the {s.overrides.join(" and ")} strategy</span>}
      </td>
      <td>{s.version ? <code>{s.version}</code> : <span className="sk-table__secondary">None</span>}</td>
      <td>{s.description}</td>
      <td>
        {s.layer === "runtime" && s.updatedAt ? (
          <>
            <time dateTime={s.updatedAt}>{formatTime(s.updatedAt)}</time>
            {s.updatedBy && <span className="sk-table__secondary">by {s.updatedBy}</span>}
          </>
        ) : (
          <span className="sk-table__secondary">Not changed here</span>
        )}
      </td>
    </tr>
  );
}
