import { useId, useState } from "react";
import { Link } from "react-router";
import { describeScope, useApproveLesson, useUpdateLesson, type Lesson } from "../../api/lessons";
import { CODE_STALE, SaveError } from "../../api/rules";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { formatTime } from "../rules/format";
import { LessonEditForm } from "./LessonEditForm";

interface LessonCardProps {
  lesson: Lesson;
  /** Whether the signed-in user may approve, edit and reject. */
  canDecide: boolean;
  /** Called with a sentence for the page to show once a lesson was approved or rejected. */
  onDecided(message: string): void;
  /** Called when a decision starts, so the page can take the last sentence away. */
  onChanging(): void;
}

/** One lesson that waits for review: its text, where it applies, where it comes from, and the choices. */
export function LessonCard({ lesson, canDecide, onDecided, onChanging }: LessonCardProps) {
  const textId = useId();
  const approve = useApproveLesson();
  const update = useUpdateLesson();

  const [editing, setEditing] = useState(false);
  const [error, setError] = useState<SaveError | null>(null);

  const busy = approve.isPending || update.isPending;

  function fail(e: Error) {
    setError(e instanceof SaveError ? e : new SaveError(0, e.message, undefined, []));
  }

  function onApprove() {
    onChanging();
    setError(null);
    approve.mutate(lesson, { onSuccess: () => onDecided("Lesson approved. Prompts use it from now on."), onError: fail });
  }

  function onReject() {
    onChanging();
    setError(null);
    update.mutate(
      { id: lesson.id, body: { version: lesson.version, text: lesson.text, scope: lesson.scope, status: "rejected" } },
      { onSuccess: () => onDecided("Lesson rejected."), onError: fail },
    );
  }

  if (editing) {
    return <LessonEditForm lesson={lesson} onDone={() => setEditing(false)} onSaved={() => onDecided("Lesson saved.")} onChanging={onChanging} />;
  }

  return (
    <article className="sk-lesson" aria-labelledby={textId}>
      <p id={textId} className="sk-lesson__text">
        {lesson.text}
      </p>
      <dl className="sk-lesson__facts">
        <div>
          <dt>Applies to</dt>
          <dd>{describeScope(lesson.scope)}</dd>
        </div>
        <div>
          <dt>Drafted</dt>
          <dd>
            <time dateTime={lesson.createdAt}>{formatTime(lesson.createdAt)}</time>
          </dd>
        </div>
        {lesson.feedback.length > 0 && (
          <div>
            <dt>From feedback on</dt>
            <dd>
              {lesson.feedback.map((f, i) => (
                <span key={f.feedbackId}>
                  {i > 0 && ", "}
                  <Link to={`/investigations/${f.investigationId}`}>investigation {f.investigationId.slice(-8)}</Link>
                </span>
              ))}
            </dd>
          </div>
        )}
      </dl>
      {error && <LessonProblem error={error} />}
      {canDecide && (
        <div className="sk-lesson__actions">
          <Button variant="primary" disabled={busy} onClick={onApprove}>
            Approve
          </Button>
          <Button
            disabled={busy}
            onClick={() => {
              onChanging();
              setError(null);
              setEditing(true);
            }}
          >
            Edit
          </Button>
          <Button variant="destructive" disabled={busy} onClick={onReject}>
            Reject
          </Button>
        </div>
      )}
    </article>
  );
}

/** Says why a decision was refused. A lesson that changed meanwhile has been read again by the page. */
function LessonProblem({ error }: { error: SaveError }) {
  if (error.code === CODE_STALE) {
    return <Alert tone="warning" title="Someone changed this lesson" />;
  }

  return (
    <Alert tone="danger" title="Could not save the decision">
      <p>{error.message}</p>
    </Alert>
  );
}
