import { useId, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router";
import { useServiceNotesList } from "../../api/notes";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { TextField } from "../../components/TextField";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import { NotesEditor } from "./NotesEditor";
import { NotesList } from "./NotesList";
import "./LessonsPage.css";

const TAB = "notes";
const SERVICE_PARAM = "service";

/**
 * The notes people wrote about services, which the memory source shows to the
 * model. Pick a service from the list or type its name; an admin edits its
 * notes as plain text, and saving empty text deletes them.
 */
export function NotesTab() {
  const me = useCurrentUser();
  const canChange = hasRole(me.role, "admin");

  const [params, setParams] = useSearchParams();
  const service = params.get(SERVICE_PARAM)?.trim() ?? "";
  const notes = useServiceNotesList();
  const listId = useId();

  const [notice, setNotice] = useState<string | null>(null);

  const services = notes.data?.pages.flatMap((p) => p.services) ?? [];

  function open(next: string) {
    setNotice(null);
    setParams(next.trim() === "" ? { tab: TAB } : { tab: TAB, [SERVICE_PARAM]: next.trim() }, { replace: true });
  }

  return (
    <>
      <p className="sk-manage__note">
        Notes are what people know about a service that an alert does not say. The memory source shows them to the model when it asks about the service.
        {!canChange && " Changing notes needs the admin role."}
      </p>
      <ServicePicker key={service} service={service} listId={listId} names={services.map((s) => s.service)} onOpen={open} />
      {notice && <Alert tone="success" title={notice} />}
      {service !== "" && <NotesEditor key={service} service={service} canChange={canChange} onChanged={setNotice} onChanging={() => setNotice(null)} />}
      {notes.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {notes.isError && <QueryError error={notes.error} onRetry={() => void notes.refetch()} />}
      {notes.isSuccess && services.length === 0 && (
        <EmptyState title="No service has notes yet">{canChange ? "Type a service name above to write the first notes." : "An admin can write notes about a service."}</EmptyState>
      )}
      {services.length > 0 && <NotesList services={services} onOpen={open} />}
      {notes.hasNextPage && (
        <div>
          <Button onClick={() => void notes.fetchNextPage()} disabled={notes.isFetchingNextPage}>
            {notes.isFetchingNextPage ? "Loading" : "Show more services"}
          </Button>
        </div>
      )}
    </>
  );
}

/** The field where a person types or picks the service whose notes they want. */
function ServicePicker({ service, listId, names, onOpen }: { service: string; listId: string; names: readonly string[]; onOpen(service: string): void }) {
  const [draft, setDraft] = useState(service);

  function submit(event: FormEvent) {
    event.preventDefault();
    onOpen(draft);
  }

  return (
    <form className="sk-filters" onSubmit={submit} aria-label="Open the notes of a service">
      <TextField
        label="Service"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        list={listId}
        autoComplete="off"
        maxLength={200}
        help="The name of the service, as alerts spell it."
      />
      <datalist id={listId}>
        {names.map((n) => (
          <option key={n} value={n} />
        ))}
      </datalist>
      <div className="sk-filters__buttons">
        <Button type="submit" variant="primary">
          Open notes
        </Button>
      </div>
    </form>
  );
}
