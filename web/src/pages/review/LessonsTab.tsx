import { useState } from "react";
import { useLessons } from "../../api/lessons";
import { Alert } from "../../components/Alert";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import { LessonCard } from "./LessonCard";
import "./LessonsTab.css";

/**
 * The lessons that wait for review, drafted from feedback. An admin approves
 * a lesson, so prompts use it, edits it first, or rejects it. Everyone else
 * can read the queue. The text of a lesson came from people and the model, so
 * it is shown as plain text.
 */
export function LessonsTab() {
  const me = useCurrentUser();
  const lessons = useLessons("proposed");
  const canDecide = hasRole(me.role, "admin");

  const [notice, setNotice] = useState<string | null>(null);

  return (
    <>
      <p>
        Lessons drafted from feedback wait here. Once an admin approves a lesson, Sidekick shows it to the model in the prompts of investigations it
        applies to.
      </p>
      {!canDecide && <p>Approving, editing and rejecting lessons needs the admin role.</p>}
      {notice && <Alert tone="success" title={notice} />}
      {lessons.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {lessons.isError && <QueryError error={lessons.error} onRetry={() => void lessons.refetch()} />}
      {lessons.isSuccess && lessons.data.lessons.length === 0 && (
        <EmptyState title="No lessons wait for review">A lesson shows up here when someone gives a reason with their feedback.</EmptyState>
      )}
      {lessons.isSuccess && lessons.data.lessons.length > 0 && (
        <>
          <p className="sk-lessons__count" role="status">
            {lessons.data.counts.proposed} {lessons.data.counts.proposed === 1 ? "lesson waits" : "lessons wait"} for review
            {lessons.data.nextCursor ? "; the newest are shown" : ""}.
          </p>
          <ul className="sk-lessons">
            {lessons.data.lessons.map((lesson) => (
              <li key={lesson.id}>
                <LessonCard lesson={lesson} canDecide={canDecide} onDecided={setNotice} onChanging={() => setNotice(null)} />
              </li>
            ))}
          </ul>
        </>
      )}
    </>
  );
}
