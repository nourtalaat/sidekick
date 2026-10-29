import { Link, useSearchParams } from "react-router";
import { useState, type FormEvent } from "react";
import { ApiError } from "../api/client";
import { useAudit, type AuditEntry, type AuditFilters } from "../api/audit";
import { Button } from "../components/Button";
import { EmptyState } from "../components/EmptyState";
import { QueryError } from "../components/QueryError";
import { Table } from "../components/Table";
import { TextField } from "../components/TextField";
import { hasRole } from "../roles";
import { useCurrentUser } from "../session";
import { formatTime } from "./rules/format";
import "./AuditPage.css";

/** The filters, by the name they have in the address of the page. */
const FILTER_NAMES = ["from", "to", "actor", "action", "investigation"] as const;

/** The actions the code writes today, offered when typing an action. */
const KNOWN_ACTIONS = [
  "listener.created",
  "listener.updated",
  "listener.deleted",
  "route.created",
  "route.updated",
  "route.deleted",
  "job.retried",
  "token.created",
  "token.revoked",
  "user.created",
  "user.role_changed",
  "watcher.started",
];

const ACTOR_KINDS: Record<AuditEntry["actorKind"], string> = {
  user: "User",
  token: "API token",
  sidekick: "Sidekick",
  policy: "Policy",
  system: "System",
};

const INVESTIGATION_ID_SHOWN = 8;

/** The audit log: who changed what and when, with the old and new values, filtered by what the address says. */
export function AuditPage() {
  const me = useCurrentUser();

  if (!hasRole(me.role, "admin")) {
    return (
      <div className="sk-page">
        <h1 className="sk-page__title">Audit log</h1>
        <QueryError error={new ApiError(403, "This needs the admin role.")} />
      </div>
    );
  }

  return <AuditLog />;
}

function AuditLog() {
  const [params, setParams] = useSearchParams();
  const filters = filtersOf(params);
  const audit = useAudit(filters);

  const entries = audit.data?.pages.flatMap((page) => page.entries) ?? [];
  const filtered = FILTER_NAMES.some((name) => filters[name] !== "");

  function apply(next: AuditFilters) {
    const search = new URLSearchParams();
    for (const name of FILTER_NAMES) {
      const value = next[name].trim();
      if (value !== "") {
        search.set(name, value);
      }
    }

    setParams(search);
  }

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Audit log</h1>
      <p>Every change people and Sidekick make, newest first. Entries are never changed or removed by hand.</p>
      <FilterForm key={params.toString()} filters={filters} onApply={apply} />
      {audit.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {audit.isError && <QueryError error={audit.error} onRetry={() => void audit.refetch()} />}
      {audit.isSuccess && entries.length === 0 && (
        <EmptyState title={filtered ? "No entries match these filters" : "Nothing recorded yet"}>
          {filtered ? "Change or clear the filters to see more." : "Changes appear here as people and Sidekick make them."}
        </EmptyState>
      )}
      {audit.isSuccess && entries.length > 0 && (
        <>
          <Table label="Audit log">
            <thead>
              <tr>
                <th scope="col">Time</th>
                <th scope="col">Who</th>
                <th scope="col">What happened</th>
                <th scope="col">To what</th>
                <th scope="col">Investigation</th>
                <th scope="col">Change</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <tr key={entry.id}>
                  <td>
                    <time dateTime={entry.at}>{formatTime(entry.at)}</time>
                  </td>
                  <td>
                    <Who entry={entry} />
                  </td>
                  <td>
                    <code>{entry.action}</code>
                  </td>
                  <td>
                    {entry.targetKind ? (
                      <>
                        {entry.targetKind} <code>{entry.targetId}</code>
                      </>
                    ) : null}
                  </td>
                  <td>
                    {entry.investigationId ? (
                      <Link to={`/investigations/${entry.investigationId}`} aria-label={`Investigation ${entry.investigationId}`}>
                        {entry.investigationId.slice(0, INVESTIGATION_ID_SHOWN)}
                      </Link>
                    ) : null}
                  </td>
                  <td>
                    <Change entry={entry} />
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
          {audit.hasNextPage && (
            <div>
              <Button onClick={() => void audit.fetchNextPage()} disabled={audit.isFetchingNextPage}>
                {audit.isFetchingNextPage ? "Loading" : "Show older entries"}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

/** The filter fields. They start from the address and change it only when the form is applied. */
function FilterForm({ filters, onApply }: { filters: AuditFilters; onApply: (next: AuditFilters) => void }) {
  const [draft, setDraft] = useState(filters);
  const set = (patch: Partial<AuditFilters>) => setDraft((d) => ({ ...d, ...patch }));

  function submit(event: FormEvent) {
    event.preventDefault();
    onApply(draft);
  }

  return (
    <form className="sk-audit-filters" onSubmit={submit} aria-label="Filter the audit log">
      <TextField label="From" type="datetime-local" value={draft.from} onChange={(e) => set({ from: e.target.value })} />
      <TextField label="To" type="datetime-local" value={draft.to} onChange={(e) => set({ to: e.target.value })} />
      <TextField
        label="Who"
        value={draft.actor}
        onChange={(e) => set({ actor: e.target.value })}
        help="A user, API token or Slack user ID, or sidekick, policy or system."
        autoComplete="off"
      />
      <TextField label="Action" list="audit-actions" value={draft.action} onChange={(e) => set({ action: e.target.value })} autoComplete="off" help="For example route.updated." />
      <datalist id="audit-actions">
        {KNOWN_ACTIONS.map((action) => (
          <option key={action} value={action} />
        ))}
      </datalist>
      <TextField
        label="Investigation ID"
        value={draft.investigation}
        onChange={(e) => set({ investigation: e.target.value })}
        autoComplete="off"
        spellCheck={false}
      />
      <div className="sk-audit-filters__buttons">
        <Button type="submit" variant="primary">
          Filter
        </Button>
        <Button
          onClick={() => {
            setDraft(EMPTY_FILTERS);
            onApply(EMPTY_FILTERS);
          }}
        >
          Clear filters
        </Button>
      </div>
    </form>
  );
}

const EMPTY_FILTERS: AuditFilters = { from: "", to: "", actor: "", action: "", investigation: "" };

function filtersOf(params: URLSearchParams): AuditFilters {
  return {
    from: params.get("from") ?? "",
    to: params.get("to") ?? "",
    actor: params.get("actor") ?? "",
    action: params.get("action") ?? "",
    investigation: params.get("investigation") ?? "",
  };
}

/** Who made the change: the name when it is known, else the ID, and what kind of actor it is. */
function Who({ entry }: { entry: AuditEntry }) {
  const kind = ACTOR_KINDS[entry.actorKind];
  const main = entry.actorName ?? entry.actorId;

  if (!main) {
    return <span>{kind}</span>;
  }

  return (
    <>
      <span>{main}</span>
      <span className="sk-table__secondary">{entry.actorName && entry.actorId ? `${kind}, ${entry.actorId}` : kind}</span>
    </>
  );
}

/** The old and new values of a change, shown on request. */
function Change({ entry }: { entry: AuditEntry }) {
  const shown: Record<string, unknown> = {};
  if (entry.old !== undefined) {
    shown.old = entry.old;
  }

  if (entry.new !== undefined) {
    shown.new = entry.new;
  }

  if (Object.keys(shown).length === 0) {
    return null;
  }

  return (
    <details>
      <summary>
        Show change<span className="sk-visually-hidden"> of {entry.action} {entry.targetKind} {entry.targetId}</span>
      </summary>
      <pre>{JSON.stringify(shown, null, 2)}</pre>
    </details>
  );
}
