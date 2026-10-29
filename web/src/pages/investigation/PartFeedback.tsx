import { MessageSquarePlus, ThumbsDown, ThumbsUp } from "lucide-react";
import { createContext, useContext, useId, useRef, useState, type ReactNode, type RefObject } from "react";
import {
  MAX_FEEDBACK_CHARS,
  partKey,
  useSubmitFeedback,
  VERDICT_CHOICES,
  type FeedbackOpinion,
  type FeedbackPart,
  type FeedbackRating,
  type FeedbackTarget,
  type NewFeedback,
} from "../../api/feedback";
import type { Verdict } from "../../api/investigations";
import { Button } from "../../components/Button";
import { Select } from "../../components/Select";
import { TextArea } from "../../components/TextArea";
import "./PartFeedback.css";

/** What the feedback controls of the page share. */
interface FeedbackValue {
  investigationId: string;
  /** Whether the user may give feedback: a responder or higher. */
  canAct: boolean;
  /** How people rated the parts, by partKey; absent while the counts are unread or could not be read. */
  parts: ReadonlyMap<string, FeedbackPart> | undefined;
  /** The verdict of the report, which the real verdict cannot be when the verdict is wrong. */
  reportedVerdict: Verdict | undefined;
}

const FeedbackContext = createContext<FeedbackValue | undefined>(undefined);

/** Gives the feedback controls below it what they need. Without it, they show nothing. */
export function FeedbackProvider({ value, children }: { value: FeedbackValue; children: ReactNode }) {
  return <FeedbackContext value={value}>{children}</FeedbackContext>;
}

/**
 * The controls for feedback on one part of the investigation: how many people
 * rated it correct and wrong and, for a responder, buttons to rate it
 * themselves. Correct is sent at once; Wrong opens a form for the reason and,
 * on the verdict, the real verdict and cause. The server checks the role
 * again.
 */
export function PartFeedback({ target, label }: { target: FeedbackTarget; label: string }) {
  const value = useContext(FeedbackContext);

  if (!value) {
    return null;
  }

  return <Controls value={value} target={target} label={label} />;
}

function Controls({ value, target, label }: { value: FeedbackValue; target: FeedbackTarget; label: string }) {
  const part = value.parts?.get(partKey(target));
  const mine = part?.mine;
  const submit = useSubmitFeedback(value.investigationId);
  const [form, setForm] = useState<FeedbackRating | null>(null);
  const [sent, setSent] = useState(false);
  const wrongButton = useRef<HTMLButtonElement>(null);
  const reasonButton = useRef<HTMLButtonElement>(null);
  const formId = useId();

  const counts = <Counts part={part} />;

  if (!value.canAct) {
    return part ? <div className="sk-feedback">{counts}</div> : null;
  }

  const rate = (rating: FeedbackRating) => {
    setSent(false);
    submit.reset();
    submit.mutate({ targetKind: target.kind, targetRef: target.ref, rating }, { onSuccess: () => setSent(true) });
  };

  const openForm = (rating: FeedbackRating) => {
    setSent(false);
    submit.reset();
    setForm(rating);
  };

  const closeForm = (sentNow: boolean, opener: RefObject<HTMLButtonElement | null>) => {
    setForm(null);
    setSent(sentNow);
    opener.current?.focus();
  };

  return (
    <div className="sk-feedback">
      <div className="sk-feedback__bar">
        {counts}
        <Button
          type="button"
          aria-label={`Mark ${label} correct`}
          aria-pressed={mine?.rating === "correct"}
          disabled={submit.isPending}
          onClick={() => rate("correct")}
        >
          <ThumbsUp size={16} aria-hidden="true" />
          Correct
        </Button>
        <Button
          ref={wrongButton}
          type="button"
          aria-label={`Mark ${label} wrong`}
          aria-pressed={mine?.rating === "wrong"}
          aria-expanded={form === "wrong"}
          aria-controls={form === "wrong" ? formId : undefined}
          disabled={submit.isPending}
          onClick={() => openForm("wrong")}
        >
          <ThumbsDown size={16} aria-hidden="true" />
          Wrong
        </Button>
        <Button
          ref={reasonButton}
          type="button"
          aria-label={`Add a reason about ${label}`}
          aria-expanded={form !== null && form !== "wrong"}
          aria-controls={form === "correct" ? formId : undefined}
          disabled={submit.isPending}
          onClick={() => openForm(mine?.rating ?? "correct")}
        >
          <MessageSquarePlus size={16} aria-hidden="true" />
          Add a reason
        </Button>
        <span role="status" className="sk-feedback__sent">
          {sent ? "Feedback sent" : ""}
        </span>
      </div>
      {form && (
        <FeedbackForm
          id={formId}
          key={form}
          target={target}
          label={label}
          rating={form}
          mine={mine}
          reportedVerdict={value.reportedVerdict}
          investigationId={value.investigationId}
          onDone={(sentNow) => closeForm(sentNow, form === "wrong" ? wrongButton : reasonButton)}
        />
      )}
      {submit.isError && !form && (
        <p role="alert" className="sk-field__error">
          {submit.error.message}
        </p>
      )}
    </div>
  );
}

/** "2 correct, 1 wrong", or that nobody has rated the part. */
function Counts({ part }: { part: FeedbackPart | undefined }) {
  return (
    <span className="sk-feedback__counts">{part ? `${part.correct} correct, ${part.wrong} wrong` : "No feedback yet"}</span>
  );
}

interface FeedbackFormProps {
  id: string;
  target: FeedbackTarget;
  label: string;
  rating: FeedbackRating;
  mine: FeedbackOpinion | undefined;
  reportedVerdict: Verdict | undefined;
  investigationId: string;
  /** Closes the form; sent says whether the feedback was sent. */
  onDone: (sent: boolean) => void;
}

/**
 * The form under a part: why it is correct or wrong and, for a Wrong on the
 * verdict, the real verdict and cause. It starts with what the user said
 * before about the same rating. A refusal of the API is shown in its own
 * words and the form keeps what was typed.
 */
function FeedbackForm({ id, target, label, rating, mine, reportedVerdict, investigationId, onDone }: FeedbackFormProps) {
  const earlier = mine?.rating === rating ? mine : undefined;
  const submit = useSubmitFeedback(investigationId);
  const [reason, setReason] = useState(earlier?.reason ?? "");
  const [verdict, setVerdict] = useState<string>(earlier?.correctVerdict ?? "");
  const [cause, setCause] = useState(earlier?.correctCause ?? "");
  const withVerdict = target.kind === "verdict" && rating === "wrong";
  const tooLong = reason.length > MAX_FEEDBACK_CHARS || cause.length > MAX_FEEDBACK_CHARS;
  const choices = VERDICT_CHOICES.filter((choice) => choice.value !== reportedVerdict);

  const send = () => {
    const body: NewFeedback = { targetKind: target.kind, targetRef: target.ref, rating };
    if (reason.trim() !== "") {
      body.reason = reason.trim();
    }

    if (withVerdict && verdict !== "") {
      body.correctVerdict = verdict as Verdict;

      if (cause.trim() !== "") {
        body.correctCause = cause.trim();
      }
    }

    submit.mutate(body, { onSuccess: () => onDone(true) });
  };

  return (
    <form
      id={id}
      className="sk-feedback__form"
      aria-label={`Feedback on ${label}`}
      onSubmit={(event) => {
        event.preventDefault();
        if (!tooLong && !submit.isPending) {
          send();
        }
      }}
    >
      <TextArea
        label={rating === "wrong" ? "Why is it wrong? (optional)" : "Why is it correct? (optional)"}
        rows={3}
        value={reason}
        autoFocus
        error={reason.length > MAX_FEEDBACK_CHARS ? `Keep the reason under ${MAX_FEEDBACK_CHARS} characters.` : undefined}
        onChange={(event) => setReason(event.target.value)}
      />
      {withVerdict && (
        <>
          <Select
            label="The real verdict, if you know it"
            help={verdict === "" ? undefined : "This confirms the investigation with this verdict."}
            value={verdict}
            options={[{ value: "", label: "I do not know" }, ...choices]}
            onChange={(event) => setVerdict(event.target.value)}
          />
          {verdict !== "" && (
            <TextArea
              label="The real cause, if you know it (optional)"
              rows={2}
              value={cause}
              error={cause.length > MAX_FEEDBACK_CHARS ? `Keep the cause under ${MAX_FEEDBACK_CHARS} characters.` : undefined}
              onChange={(event) => setCause(event.target.value)}
            />
          )}
        </>
      )}
      {submit.isError && (
        <p role="alert" className="sk-field__error">
          {submit.error.message}
        </p>
      )}
      <div className="sk-feedback__actions">
        <Button type="submit" variant="primary" disabled={tooLong || submit.isPending}>
          {submit.isPending ? "Sending" : "Send feedback"}
        </Button>
        <Button type="button" disabled={submit.isPending} onClick={() => onDone(false)}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
