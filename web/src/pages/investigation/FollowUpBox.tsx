import { Send } from "lucide-react";
import { useState, type Ref } from "react";
import { useAskFollowUp } from "../../api/investigations";
import { Button } from "../../components/Button";
import "./FollowUpBox.css";

/** The longest follow-up question the API takes, in bytes. */
export const MAX_QUESTION_BYTES = 4000;

/**
 * The box at the bottom of the main column where a responder asks a
 * follow-up question. A refusal of the API, such as for a triage-only
 * result, is shown in its own words. The page shows this box only to
 * responders and above; the server checks the role again.
 */
export function FollowUpBox({ investigationId, textareaRef }: { investigationId: string; textareaRef?: Ref<HTMLTextAreaElement> }) {
  const [text, setText] = useState("");
  const ask = useAskFollowUp(investigationId);
  const tooLong = new TextEncoder().encode(text.trim()).length > MAX_QUESTION_BYTES;
  const canSend = text.trim() !== "" && !tooLong && !ask.isPending;

  return (
    <form
      className="sk-followup"
      aria-label="Ask a follow-up"
      onSubmit={(event) => {
        event.preventDefault();
        if (!canSend) {
          return;
        }

        ask.mutate(text.trim(), { onSuccess: () => setText("") });
      }}
    >
      <label htmlFor="followup-text" className="sk-field__label">
        Ask a follow-up
      </label>
      <textarea
        id="followup-text"
        ref={textareaRef}
        className="sk-field__input sk-followup__text"
        rows={2}
        value={text}
        aria-invalid={tooLong || undefined}
        onChange={(event) => setText(event.target.value)}
      />
      <div className="sk-followup__bar">
        <div className="sk-followup__messages">
          {tooLong && <p className="sk-field__error">The question is too long; keep it under {MAX_QUESTION_BYTES} bytes.</p>}
          {ask.isError && (
            <p role="alert" className="sk-field__error">
              {ask.error.message}
            </p>
          )}
          {ask.isSuccess && (
            <p role="status" className="sk-followup__sent">
              Question sent. Sidekick answers in a new run.
            </p>
          )}
        </div>
        <Button type="submit" variant="primary" disabled={!canSend}>
          <Send size={16} aria-hidden="true" />
          {ask.isPending ? "Sending" : "Send question"}
        </Button>
      </div>
    </form>
  );
}
