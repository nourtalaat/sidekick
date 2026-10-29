import { Plus } from "lucide-react";
import { useState } from "react";
import { Link, useLocation } from "react-router";
import { useDeleteListener, useListeners, type Listener } from "../api/rules";
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

/** The listeners of the config file and those added at runtime, with the buttons to change the runtime ones. */
export function ListenersPage() {
  const me = useCurrentUser();
  const location = useLocation();
  const listeners = useListeners();
  const remove = useDeleteListener();
  const canEdit = hasRole(me.role, "responder");

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
      <h1 className="sk-page__title">Listeners</h1>
      <p>
        A listener watches a chat channel for alerts that bots or people post, and turns them into events. A route picks the events of a listener by its name.
        Listeners in the config file cannot be changed here.
      </p>
      {canEdit ? (
        <div>
          <LinkButton to="/listeners/new" variant="primary">
            <Plus size={16} aria-hidden="true" />
            Add a listener
          </LinkButton>
        </div>
      ) : (
        <p>Adding and changing listeners needs the responder role.</p>
      )}
      {saved && !deleted && <Alert tone="success" title={saved} />}
      {deleted && <Alert tone="success" title={`Listener ${deleted} deleted.`} />}
      {remove.isError && !deleted && <Alert tone="danger" title="Could not delete the listener">
          <p>{remove.error.message}</p>
        </Alert>}
      {listeners.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {listeners.isError && <QueryError error={listeners.error} onRetry={() => void listeners.refetch()} />}
      {listeners.isSuccess && listeners.data.length === 0 && (
        <EmptyState title="No listeners yet">Add a listener to turn the alerts of a channel into events.</EmptyState>
      )}
      {listeners.isSuccess && listeners.data.length > 0 && (
        <Table label="Listeners">
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Status</th>
              <th scope="col">Where</th>
              <th scope="col">Messages</th>
              <th scope="col">Changed</th>
              <th scope="col">
                <span className="sk-visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {listeners.data.map((l) => {
              const key = `${l.layer}/${l.name}`;

              return (
                <tr key={key}>
                  <th scope="row">{l.name}</th>
                  <td>
                    <RuleStatusBadge status={l.status} reason={l.statusReason} />
                  </td>
                  <td>
                    <RuleLayerBadge layer={l.layer} />
                  </td>
                  <td>
                    <Messages listener={l} />
                  </td>
                  <td>{l.layer === "runtime" ? <Changed by={l.updatedBy} at={l.updatedAt} /> : <span className="sk-table__secondary">Config file</span>}</td>
                  <td>
                    {canEdit && l.layer === "runtime" && (
                      <div className="sk-table__actions">
                        {confirming === key ? (
                          <>
                            <span>Delete {l.name}?</span>
                            <Button variant="destructive" disabled={remove.isPending} onClick={() => confirmDelete(l.name)}>
                              Delete listener {l.name}
                            </Button>
                            <Button onClick={() => setConfirming(null)}>Cancel</Button>
                          </>
                        ) : (
                          <>
                            {l.status !== "shadowed" && (
                              <Link to={`/listeners/${encodeURIComponent(l.name)}`} aria-label={`Change listener ${l.name}`}>
                                Change
                              </Link>
                            )}
                            <Button
                              onClick={() => {
                                setDeleted(null);
                                remove.reset();
                                setConfirming(key);
                              }}
                              aria-label={`Delete listener ${l.name}`}
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

/** Where a listener listens and which of the messages there it takes. */
function Messages({ listener }: { listener: Listener }) {
  const sender = listener.match?.sender;
  const names = [...(sender?.names ?? []), ...(sender?.bots ?? []), ...(sender?.users ?? [])];
  const kind = sender?.kind === "bot" ? "bots" : sender?.kind === "human" ? "people" : undefined;
  const text = listener.match?.text;

  let who = "every sender";
  if (names.length > 0) {
    who = names.join(", ");
  } else if (kind) {
    who = kind;
  }

  return (
    <>
      <span>
        {listener.channel}: messages from {who}
      </span>
      {text && (
        <span className="sk-table__secondary">
          Matching <code>{text}</code>
        </span>
      )}
    </>
  );
}

function Changed({ by, at }: { by?: string; at?: string }) {
  return (
    <>
      {at && <time dateTime={at}>{formatTime(at)}</time>}
      {by && <span className="sk-table__secondary">by {by}</span>}
    </>
  );
}
