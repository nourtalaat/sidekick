import { useId, useState } from "react";
import { Link } from "react-router";
import { describeScope, useDeleteLesson, useUpdateLesson, type Lesson } from "../../api/lessons";
import type { SaveError } from "../../api/rules";
import { Button } from "../../components/Button";
import { DeleteConfirm } from "../../components/DeleteConfirm";
import { StatusBadge, type Status } from "../../components/Badge";
import { formatTime } from "../rules/format";
import { LessonEditForm } from "../review/LessonEditForm";
import { ProblemAlert, saveErrorOf } from "./ProblemAlert";
import "../review/LessonsTab.css";
import "./LessonsPage.css";

const STATUS_BADGES: Record<Lesson["status"], { status: Status; label: string }> = {
  active: { status: "success", label: "Active" },
  proposed: { status: "warning", label: "Waits for review" },
  disabled: { status: "neutral", label: "Disabled" },
  rejected: { status: "danger", label: "Rejected" },
};

const ORIGINS: Record<Lesson["origin"], string> = {
  feedback: "Drafted from feedback",
  ui: "Written in the web UI",
  slack: "Written in Slack",
  file: "From the config file",
};

interface LessonRowProps {
  lesson: Lesson;
  /** Whether the signed-in user may edit, disable, enable and delete. */
  canChange: boolean;
  /** Called with a sentence for the page to show once a lesson changed. */
  onChanged(message: string): void;
  /** Called when a change starts, so the page can take the last sentence away. */
  onChanging(): void;
}

/** One lesson: its text, status, scope, how often people found it helpful, and the changes an admin can make. */
export function LessonRow({ lesson, canChange, onChanged, onChanging }: LessonRowProps) {
  const textId = useId();
  const update = useUpdateLesson();
  const remove = useDeleteLesson();

  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<SaveError | null>(null);

  const badge = STATUS_BADGES[lesson.status];
  const busy = update.isPending || remove.isPending;
  const canEdit = lesson.status === "active" || lesson.status === "disabled";

  function begin() {
    onChanging();
    setError(null);
  }

  function setStatus(status: "active" | "disabled") {
    begin();
    update.mutate(
      { id: lesson.id, body: { version: lesson.version, text: lesson.text, scope: lesson.scope, status } },
      { onSuccess: () => onChanged(status === "active" ? "Lesson enabled. Prompts use it again." : "Lesson disabled. Prompts no longer use it."), onError: (e) => setError(saveErrorOf(e)) },
    );
  }

  function onDelete() {
    begin();
    remove.mutate(lesson.id, {
      onSuccess: () => onChanged("Lesson deleted."),
      onError: (e) => {
        setConfirming(false);
        setError(saveErrorOf(e));
      },
    });
  }

  if (editing) {
    return <LessonEditForm lesson={lesson} onDone={() => setEditing(false)} onSaved={() => onChanged("Lesson saved.")} onChanging={onChanging} />;
  }

  return (
    <article className="sk-lesson" aria-labelledby={textId}>
      <div className="sk-manage__badges">
        <StatusBadge status={badge.status}>{badge.label}</StatusBadge>
        <span className="sk-manage__note">{ORIGINS[lesson.origin]}</span>
      </div>
      <p id={textId} className="sk-lesson__text">
        {lesson.text}
      </p>
      <dl className="sk-lesson__facts">
        <div>
          <dt>Applies to</dt>
          <dd>{describeScope(lesson.scope)}</dd>
        </div>
        <div>
          <dt>Found helpful</dt>
          <dd>{lesson.helpful}</dd>
        </div>
        <div>
          <dt>Found unhelpful</dt>
          <dd>{lesson.unhelpful}</dd>
        </div>
        <div>
          <dt>Last used</dt>
          <dd>{lesson.lastUsedAt ? <time dateTime={lesson.lastUsedAt}>{formatTime(lesson.lastUsedAt)}</time> : "Never"}</dd>
        </div>
        <div>
          <dt>Created</dt>
          <dd>
            <time dateTime={lesson.createdAt}>{formatTime(lesson.createdAt)}</time>
          </dd>
        </div>
      </dl>
      {lesson.status === "proposed" && (
        <p className="sk-manage__note">
          This lesson waits for a decision. <Link to="/review?tab=lessons">Review it in the review queue</Link>.
        </p>
      )}
      {error && <ProblemAlert error={error} noun="lesson" />}
      {canChange && !confirming && (
        <div className="sk-lesson__actions">
          {canEdit && (
            <Button
              disabled={busy}
              onClick={() => {
                begin();
                setEditing(true);
              }}
              aria-label={`Edit lesson: ${lesson.text}`}
            >
              Edit
            </Button>
          )}
          {lesson.status === "active" && (
            <Button disabled={busy} onClick={() => setStatus("disabled")} aria-label={`Disable lesson: ${lesson.text}`}>
              Disable
            </Button>
          )}
          {lesson.status === "disabled" && (
            <Button disabled={busy} onClick={() => setStatus("active")} aria-label={`Enable lesson: ${lesson.text}`}>
              Enable
            </Button>
          )}
          <Button
            disabled={busy}
            onClick={() => {
              begin();
              setConfirming(true);
            }}
            aria-label={`Delete lesson: ${lesson.text}`}
          >
            Delete
          </Button>
        </div>
      )}
      {canChange && confirming && (
        <DeleteConfirm subject="lesson" busy={remove.isPending} onConfirm={onDelete} onCancel={() => setConfirming(false)}>
          <p>The lesson and the record of the investigations that used it are removed. The audit log keeps its text.</p>
        </DeleteConfirm>
      )}
    </article>
  );
}
