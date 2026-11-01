import { useState } from "react";
import { useSearchParams } from "react-router";
import { useMemoryEntries, type DisabledFilter, type MemoryFilters } from "../../api/memory";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import { EMPTY_MEMORY_FILTERS, MemoryFilterForm, VERDICT_LABELS } from "./MemoryFilterForm";
import { MemoryEntryRow } from "./MemoryEntryRow";
import "../review/LessonsTab.css";
import "./LessonsPage.css";

const TAB = "memory";
const DISABLED_VALUES: readonly DisabledFilter[] = ["enabled", "disabled"];

/**
 * The memory of confirmed investigations, newest confirmation first. An admin
 * can fix the summary and cause of an entry, disable it so prompts never use
 * it, or delete it. The text of an entry came from the model and from people,
 * so it is shown as plain text.
 */
export function MemoryTab() {
  const me = useCurrentUser();
  const canChange = hasRole(me.role, "admin");

  const [params, setParams] = useSearchParams();
  const filters = memoryFiltersOf(params);
  const filtered = Object.values(filters).some((v) => v !== "");
  const memory = useMemoryEntries(filters);

  const [notice, setNotice] = useState<string | null>(null);

  const entries = memory.data?.pages.flatMap((p) => p.entries) ?? [];

  function apply(next: MemoryFilters) {
    const nextParams = new URLSearchParams({ tab: TAB });
    for (const [key, value] of Object.entries(next)) {
      if (value !== "") {
        nextParams.set(key, value);
      }
    }

    setParams(nextParams, { replace: true });
  }

  return (
    <>
      <p className="sk-manage__note">
        Memory has one entry for each investigation whose verdict a responder confirmed. Prompts show the entries that look like a new alert.
        {!canChange && " Changing entries needs the admin role."}
      </p>
      {notice && <Alert tone="success" title={notice} />}
      <MemoryFilterForm key={JSON.stringify(filters)} filters={filters} onApply={apply} />
      {memory.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {memory.isError && <QueryError error={memory.error} onRetry={() => void memory.refetch()} />}
      {memory.isSuccess && entries.length === 0 && (
        <EmptyState title={filtered ? "No entries match these filters" : "Memory is empty"}>
          {filtered ? "Change or clear the filters to see more." : "An entry appears here when a responder confirms the verdict of an investigation."}
        </EmptyState>
      )}
      {entries.length > 0 && (
        <ul className="sk-lessons">
          {entries.map((entry) => (
            <li key={entry.investigationId}>
              <MemoryEntryRow entry={entry} canChange={canChange} onChanged={setNotice} onChanging={() => setNotice(null)} />
            </li>
          ))}
        </ul>
      )}
      {memory.hasNextPage && (
        <div>
          <Button onClick={() => void memory.fetchNextPage()} disabled={memory.isFetchingNextPage}>
            {memory.isFetchingNextPage ? "Loading" : "Show older entries"}
          </Button>
        </div>
      )}
    </>
  );
}

/** The filters the address holds. A verdict or state it does not know is left out. */
function memoryFiltersOf(params: URLSearchParams): MemoryFilters {
  const verdict = params.get("verdict");
  const disabled = params.get("disabled");

  return {
    ...EMPTY_MEMORY_FILTERS,
    service: params.get("service") ?? "",
    verdict: Object.keys(VERDICT_LABELS).find((v) => v === verdict) as MemoryFilters["verdict"] | undefined ?? "",
    q: params.get("q") ?? "",
    disabled: DISABLED_VALUES.find((d) => d === disabled) ?? "",
  };
}
