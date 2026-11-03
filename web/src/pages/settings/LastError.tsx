import { useId, useState } from "react";
import { Button } from "../../components/Button";
import { errorPreview } from "./jobError";
import "./JobsTab.css";

/**
 * The last error of a job as plain text, cut to its first lines. When
 * something was cut, a button shows the whole text and hides it again. The
 * text is never HTML, whatever it holds.
 */
export function LastError({ jobId, text }: { jobId: number; text: string | undefined }) {
  const textId = useId();
  const [open, setOpen] = useState(false);

  if (text === undefined || text === "") {
    return <span className="sk-jobs__muted">No error recorded</span>;
  }

  const preview = errorPreview(text);
  const word = open ? "less" : "all";

  return (
    <>
      <pre id={textId} className="sk-jobs__error">
        {open ? text : preview.text}
      </pre>
      {preview.cut && (
        <Button aria-expanded={open} aria-controls={textId} aria-label={`Show ${word} of the last error of job ${jobId}`} onClick={() => setOpen(!open)}>
          Show {word}
        </Button>
      )}
    </>
  );
}
