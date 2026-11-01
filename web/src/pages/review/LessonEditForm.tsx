import { useState, type FormEvent } from "react";
import { MAX_LESSON_CHARS, useUpdateLesson, type Lesson } from "../../api/lessons";
import { CODE_STALE, SaveError } from "../../api/rules";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { TextArea } from "../../components/TextArea";
import { TextField } from "../../components/TextField";
import { sortErrors } from "../rules/formHelpers";

const FIELDS = ["text", "scope.service", "scope.alert", "scope.route", "scope.strategy"] as const;

interface LessonEditFormProps {
  lesson: Lesson;
  onDone(): void;
  onSaved(): void;
  onChanging(): void;
}

/** The form that edits the text and scope of a lesson that waits for review. It stays a lesson that waits. */
export function LessonEditForm({ lesson, onDone, onSaved, onChanging }: LessonEditFormProps) {
  const update = useUpdateLesson();

  const [text, setText] = useState(lesson.text);
  const [service, setService] = useState(lesson.scope.service ?? "");
  const [alertName, setAlertName] = useState(lesson.scope.alert ?? "");
  const [route, setRoute] = useState(lesson.scope.route ?? "");
  const [strategy, setStrategy] = useState(lesson.scope.strategy ?? "");
  const [error, setError] = useState<SaveError | null>(null);

  const errors = sortErrors(error?.fields ?? [], FIELDS);
  const stale = error?.code === CODE_STALE;
  const length = [...text].length;

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    onChanging();
    setError(null);

    update.mutate(
      {
        id: lesson.id,
        body: {
          version: lesson.version,
          text: text.trim(),
          scope: { service: service.trim() || undefined, alert: alertName.trim() || undefined, route: route.trim() || undefined, strategy: strategy.trim() || undefined },
        },
      },
      {
        onSuccess: () => {
          onSaved();
          onDone();
        },
        onError: (e: Error) => setError(e instanceof SaveError ? e : new SaveError(0, e.message, undefined, [])),
      },
    );
  }

  return (
    <form className="sk-lesson sk-lesson--editing" onSubmit={onSubmit} aria-label="Edit lesson">
      {stale && (
        <Alert tone="warning" title="Someone changed this lesson">
          <p>The queue has the new version. Cancel and edit that one.</p>
        </Alert>
      )}
      {error && !stale && errors.other.length > 0 && (
        <Alert tone="danger" title="Could not save the lesson">
          {errors.other.map((message) => (
            <p key={message}>{message}</p>
          ))}
        </Alert>
      )}
      {error && !stale && errors.other.length === 0 && Object.keys(errors.byField).length === 0 && (
        <Alert tone="danger" title="Could not save the lesson">
          <p>{error.message}</p>
        </Alert>
      )}
      <TextArea
        label="Lesson"
        rows={4}
        value={text}
        onChange={(e) => setText(e.target.value)}
        help={`${length} of ${MAX_LESSON_CHARS} characters`}
        error={errors.byField["text"]}
        required
      />
      <fieldset className="sk-lesson__scope">
        <legend>Applies to</legend>
        <p className="sk-lesson__scope-help">A lesson from feedback needs at least one of these. It applies when every one that is filled in matches.</p>
        <TextField label="Service" value={service} onChange={(e) => setService(e.target.value)} error={errors.byField["scope.service"]} />
        <TextField label="Alert name" value={alertName} onChange={(e) => setAlertName(e.target.value)} error={errors.byField["scope.alert"]} />
        <TextField label="Route" value={route} onChange={(e) => setRoute(e.target.value)} error={errors.byField["scope.route"]} />
        <TextField label="Strategy" value={strategy} onChange={(e) => setStrategy(e.target.value)} error={errors.byField["scope.strategy"]} />
      </fieldset>
      <div className="sk-lesson__actions">
        <Button type="submit" variant="primary" disabled={update.isPending || stale}>
          Save lesson
        </Button>
        <Button onClick={onDone}>Cancel</Button>
      </div>
    </form>
  );
}
