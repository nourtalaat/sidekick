import { useState, type FormEvent } from "react";
import { MAX_LESSON_CHARS, useCreateLesson } from "../../api/lessons";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { TextArea } from "../../components/TextArea";
import { TextField } from "../../components/TextField";
import { sortErrors } from "../rules/formHelpers";
import { saveErrorOf } from "./ProblemAlert";
import "../review/LessonsTab.css";

const FIELDS = ["text", "scope.service", "scope.alert", "scope.route", "scope.strategy"] as const;

/** The form that writes a lesson. With no scope field filled in, the lesson is global and applies to every investigation. */
export function NewLessonForm({ onDone, onCreated }: { onDone(): void; onCreated(): void }) {
  const create = useCreateLesson();

  const [text, setText] = useState("");
  const [service, setService] = useState("");
  const [alertName, setAlertName] = useState("");
  const [route, setRoute] = useState("");
  const [strategy, setStrategy] = useState("");

  const error = create.error ? saveErrorOf(create.error) : null;
  const errors = sortErrors(error?.fields ?? [], FIELDS);
  const length = [...text].length;
  const global = ![service, alertName, route, strategy].some((v) => v.trim() !== "");

  function onSubmit(event: FormEvent) {
    event.preventDefault();

    create.mutate(
      {
        text: text.trim(),
        status: "active",
        scope: { service: service.trim() || undefined, alert: alertName.trim() || undefined, route: route.trim() || undefined, strategy: strategy.trim() || undefined },
      },
      {
        onSuccess: () => {
          onCreated();
          onDone();
        },
      },
    );
  }

  return (
    <form className="sk-lesson sk-lesson--editing" onSubmit={onSubmit} aria-label="Write a lesson">
      {error && (errors.other.length > 0 || Object.keys(errors.byField).length === 0) && (
        <Alert tone="danger" title="Could not save the lesson">
          {errors.other.length > 0 ? errors.other.map((message) => <p key={message}>{message}</p>) : <p>{error.message}</p>}
        </Alert>
      )}
      <TextArea
        label="Lesson"
        rows={4}
        value={text}
        onChange={(e) => setText(e.target.value)}
        help={`${length} of ${MAX_LESSON_CHARS} characters. Prompts show it to the model as it is written.`}
        error={errors.byField["text"]}
        required
      />
      <fieldset className="sk-lesson__scope">
        <legend>Applies to</legend>
        <p className="sk-lesson__scope-help">
          {global
            ? "No field is filled in, so this lesson is global and applies to every investigation."
            : "It applies when every field that is filled in matches the investigation."}
        </p>
        <TextField label="Service" value={service} onChange={(e) => setService(e.target.value)} error={errors.byField["scope.service"]} />
        <TextField label="Alert name" value={alertName} onChange={(e) => setAlertName(e.target.value)} error={errors.byField["scope.alert"]} />
        <TextField label="Route" value={route} onChange={(e) => setRoute(e.target.value)} error={errors.byField["scope.route"]} />
        <TextField label="Strategy" value={strategy} onChange={(e) => setStrategy(e.target.value)} error={errors.byField["scope.strategy"]} />
      </fieldset>
      <div className="sk-lesson__actions">
        <Button type="submit" variant="primary" disabled={create.isPending}>
          Save lesson
        </Button>
        <Button onClick={onDone}>Cancel</Button>
      </div>
    </form>
  );
}
