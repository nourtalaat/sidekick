import { useState } from "react";
import { useSearchParams } from "react-router";
import { useLessonList, type LessonFilters, type LessonStatus } from "../../api/lessons";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import { EMPTY_LESSON_FILTERS, LessonFilterForm } from "./LessonFilterForm";
import { LessonRow } from "./LessonRow";
import { NewLessonForm } from "./NewLessonForm";
import "../review/LessonsTab.css";
import "./LessonsPage.css";

const STATUSES: readonly LessonStatus[] = ["proposed", "active", "disabled", "rejected"];
const TAB = "lessons";

/**
 * Every lesson, with filters, counts of how often people found it helpful,
 * and for an admin the changes: write, edit, disable, enable and delete. The
 * text of a lesson came from people and the model, so it is shown as plain
 * text.
 */
export function LessonsTab() {
  const me = useCurrentUser();
  const canChange = hasRole(me.role, "admin");

  const [params, setParams] = useSearchParams();
  const filters = lessonFiltersOf(params);
  const filtered = filters.status !== "" || filters.service !== "" || filters.q !== "";
  const lessons = useLessonList(filters);

  const [writing, setWriting] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  const items = lessons.data?.pages.flatMap((p) => p.lessons) ?? [];
  const counts = lessons.data?.pages[0]?.counts;

  function apply(next: LessonFilters) {
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
      <div className="sk-manage__head">
        <p className="sk-manage__note">
          Lessons are short pieces of guidance that Sidekick shows the model in the prompts of investigations they apply to.
          {!canChange && " Changing lessons needs the admin role."}
        </p>
        {canChange && !writing && (
          <Button
            variant="primary"
            onClick={() => {
              setNotice(null);
              setWriting(true);
            }}
          >
            Write a lesson
          </Button>
        )}
      </div>
      {writing && <NewLessonForm onDone={() => setWriting(false)} onCreated={() => setNotice("Lesson saved. Prompts use it from now on.")} />}
      {notice && <Alert tone="success" title={notice} />}
      <LessonFilterForm key={JSON.stringify(filters)} filters={filters} onApply={apply} />
      {counts && (
        <p className="sk-manage__counts" role="status">
          {STATUSES.map((s) => `${counts[s]} ${STATUS_WORDS[s]}`).join(", ")}
        </p>
      )}
      {lessons.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {lessons.isError && <QueryError error={lessons.error} onRetry={() => void lessons.refetch()} />}
      {lessons.isSuccess && items.length === 0 && (
        <EmptyState title={filtered ? "No lessons match these filters" : "No lessons yet"}>
          {filtered ? "Change or clear the filters to see more." : "A lesson shows up here when someone gives a reason with their feedback, or an admin writes one."}
        </EmptyState>
      )}
      {items.length > 0 && (
        <ul className="sk-lessons">
          {items.map((lesson) => (
            <li key={lesson.id}>
              <LessonRow lesson={lesson} canChange={canChange} onChanged={setNotice} onChanging={() => setNotice(null)} />
            </li>
          ))}
        </ul>
      )}
      {lessons.hasNextPage && (
        <div>
          <Button onClick={() => void lessons.fetchNextPage()} disabled={lessons.isFetchingNextPage}>
            {lessons.isFetchingNextPage ? "Loading" : "Show older lessons"}
          </Button>
        </div>
      )}
    </>
  );
}

const STATUS_WORDS: Record<LessonStatus, string> = { proposed: "wait for review", active: "active", disabled: "disabled", rejected: "rejected" };

/** The filters the address holds. A status it does not know is left out. */
function lessonFiltersOf(params: URLSearchParams): LessonFilters {
  const status = params.get("status");

  return {
    ...EMPTY_LESSON_FILTERS,
    status: STATUSES.find((s) => s === status) ?? "",
    service: params.get("service") ?? "",
    q: params.get("q") ?? "",
  };
}
