import { Plus } from "lucide-react";
import { useState } from "react";
import { Link, useLocation } from "react-router";
import { useDeleteRoute, useRoutes, type Route } from "../api/rules";
import { Alert } from "../components/Alert";
import { Button, LinkButton } from "../components/Button";
import { EmptyState } from "../components/EmptyState";
import { QueryError } from "../components/QueryError";
import { Table } from "../components/Table";
import { hasRole } from "../roles";
import { useCurrentUser } from "../session";
import { formatTime } from "./rules/format";
import { RuleLayerBadge, RuleStatusBadge } from "./rules/RuleBadges";

const SAVED_STATE_KEY = "saved";

/** The routes of the config file and those added at runtime, in the order they are tried. */
export function RoutesPage() {
  const me = useCurrentUser();
  const location = useLocation();
  const routes = useRoutes();
  const remove = useDeleteRoute();
  const canEdit = hasRole(me.role, "admin");

  const [confirming, setConfirming] = useState<string | null>(null);
  const [deleted, setDeleted] = useState<string | null>(null);

  const saved = (location.state as Record<string, string> | null)?.[SAVED_STATE_KEY];

  function confirmDelete(name: string) {
    remove.mutate(name, {
      onSuccess: () => {
        setConfirming(null);
        setDeleted(name);
      },
      onError: () => setConfirming(null),
    });
  }

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Routes</h1>
      <p>
        A route picks the events it matches and says how Sidekick investigates them. The first route that matches takes the event, so they are listed in the
        order they are tried. Routes in the config file cannot be changed here.
      </p>
      {canEdit ? (
        <div>
          <LinkButton to="/routes/new" variant="primary">
            <Plus size={16} aria-hidden="true" />
            Add a route
          </LinkButton>
        </div>
      ) : (
        <p>Adding and changing routes needs the admin role.</p>
      )}
      {saved && !deleted && <Alert tone="success" title={saved} />}
      {deleted && <Alert tone="success" title={`Route ${deleted} deleted.`} />}
      {remove.isError && !deleted && (
        <Alert tone="danger" title="Could not delete the route">
          <p>{remove.error.message}</p>
        </Alert>
      )}
      {routes.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {routes.isError && <QueryError error={routes.error} onRetry={() => void routes.refetch()} />}
      {routes.isSuccess && routes.data.length === 0 && <EmptyState title="No routes">The config file defines at least the default route.</EmptyState>}
      {routes.isSuccess && routes.data.length > 0 && (
        <Table label="Routes">
          <thead>
            <tr>
              <th scope="col">Order</th>
              <th scope="col">Name</th>
              <th scope="col">Status</th>
              <th scope="col">Where</th>
              <th scope="col">Matches</th>
              <th scope="col">Investigates</th>
              <th scope="col">Changed</th>
              <th scope="col">
                <span className="sk-visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {routes.data.map((r) => {
              const key = `${r.layer}/${r.name}`;

              return (
                <tr key={key}>
                  <td>{r.position ?? <span className="sk-table__secondary">Not tried</span>}</td>
                  <th scope="row">{r.name}</th>
                  <td>
                    <RuleStatusBadge status={r.status} reason={r.statusReason} />
                  </td>
                  <td>
                    <RuleLayerBadge layer={r.layer} />
                  </td>
                  <td>
                    <Matches route={r} />
                  </td>
                  <td>
                    <Investigates route={r} />
                  </td>
                  <td>
                    {r.layer === "runtime" ? (
                      <>
                        {r.updatedAt && <time dateTime={r.updatedAt}>{formatTime(r.updatedAt)}</time>}
                        {r.updatedBy && <span className="sk-table__secondary">by {r.updatedBy}</span>}
                      </>
                    ) : (
                      <span className="sk-table__secondary">Config file</span>
                    )}
                  </td>
                  <td>
                    {canEdit && r.layer === "runtime" && (
                      <div className="sk-table__actions">
                        {confirming === key ? (
                          <>
                            <span>Delete {r.name}?</span>
                            <Button variant="destructive" disabled={remove.isPending} onClick={() => confirmDelete(r.name)}>
                              Delete route {r.name}
                            </Button>
                            <Button onClick={() => setConfirming(null)}>Cancel</Button>
                          </>
                        ) : (
                          <>
                            {r.status !== "shadowed" && (
                              <Link to={`/routes/${encodeURIComponent(r.name)}`} aria-label={`Change route ${r.name}`}>
                                Change
                              </Link>
                            )}
                            <Button
                              onClick={() => {
                                setDeleted(null);
                                remove.reset();
                                setConfirming(key);
                              }}
                              aria-label={`Delete route ${r.name}`}
                            >
                              Delete
                            </Button>
                          </>
                        )}
                      </div>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </Table>
      )}
    </div>
  );
}

/** What a route matches, one part to a line, or "Every event" for the default route. */
function Matches({ route }: { route: Route }) {
  const m = route.match;
  const parts: string[] = [];

  if (m?.intake?.length) {
    parts.push(`Intake ${m.intake.join(", ")}`);
  }

  if (m?.service) {
    parts.push(`Service ${m.service}`);
  }

  if (m?.severity?.length) {
    parts.push(`Severity ${m.severity.join(", ")}`);
  }

  for (const [name, value] of Object.entries(m?.labels ?? {})) {
    parts.push(`Label ${name}=${value}`);
  }

  if (m?.text) {
    parts.push(`Text ${m.text}`);
  }

  if (parts.length === 0) {
    return <span>Every event</span>;
  }

  return (
    <>
      {parts.map((part) => (
        <span key={part} className="sk-table__line">
          {part}
        </span>
      ))}
    </>
  );
}

/** How a route investigates: strategy, mode, and the sources and outputs it uses. */
function Investigates({ route }: { route: Route }) {
  return (
    <>
      <span className="sk-table__line">
        Strategy {route.strategy ?? "default"}, mode {route.mode ?? "read-only"}
      </span>
      {route.sources?.length ? <span className="sk-table__secondary">Sources {route.sources.join(", ")}</span> : null}
      {route.outputs?.length ? <span className="sk-table__secondary">Outputs {route.outputs.join(", ")}</span> : null}
    </>
  );
}
