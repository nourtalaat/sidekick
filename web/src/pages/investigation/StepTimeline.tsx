import { ChevronDown, ChevronRight } from "lucide-react";
import { useId, useState } from "react";
import type { Step, StepPart } from "../../api/investigations";
import { Button } from "../../components/Button";
import { CopyButton } from "../../components/CopyButton";
import { formatDuration, formatTokens } from "../../format";
import "./StepTimeline.css";

const KIND_LABELS: Record<Step["kind"], string> = {
  prompt: "Prompt",
  model_turn: "Model turn",
  tool_result: "Tool result",
  note: "Note",
  inbox: "New information",
  summary: "Summary",
  triage_prompt: "Triage prompt",
  triage_turn: "Triage turn",
  triage_result: "Triage result",
  triage_override: "Full investigation asked for",
};

interface StepTimelineProps {
  steps: readonly Step[];
  /** Whether a run is going, so the list tells assistive technology about new steps. */
  live: boolean;
  /** Whether the API has more steps than the list has. */
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore(): void;
}

/**
 * The record of the run, one line for each step, collapsed. A step opens to
 * show its parts in a sunken box with a copy button. Everything in a step
 * came from the model, a source or an alert, so it is plain text. The list
 * is a polite live region while a run goes; new steps reach it in batches.
 */
export function StepTimeline({ steps, live, hasMore, loadingMore, onLoadMore }: StepTimelineProps) {
  return (
    <section className="sk-steps" aria-labelledby="steps-heading">
      <h2 id="steps-heading" className="sk-steps__title">
        Step by step
      </h2>
      {steps.length === 0 ? (
        <p className="sk-steps__none">{live ? "No step yet." : "This investigation has no saved steps."}</p>
      ) : (
        <ol className="sk-steps__list" aria-labelledby="steps-heading" aria-live={live ? "polite" : "off"} aria-relevant="additions">
          {steps.map((step) => (
            <StepRow key={step.seq} step={step} />
          ))}
        </ol>
      )}
      {hasMore && (
        <Button onClick={onLoadMore} disabled={loadingMore}>
          {loadingMore ? "Loading more steps" : "Load more steps"}
        </Button>
      )}
    </section>
  );
}

function StepRow({ step }: { step: Step }) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const Chevron = open ? ChevronDown : ChevronRight;

  return (
    <li className="sk-step">
      <button type="button" className="sk-step__toggle" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen(!open)}>
        <Chevron size={16} aria-hidden="true" />
        <span className="sk-step__summary">{summaryOf(step)}</span>
        <span className="sk-step__meta">{metaOf(step)}</span>
      </button>
      {open && (
        <div id={panelId} className="sk-step__body">
          {step.error && <p className="sk-step__error">{step.error}</p>}
          {step.parts.map((part, i) => (
            <PartBox key={i} part={part} />
          ))}
        </div>
      )}
    </li>
  );
}

function PartBox({ part }: { part: StepPart }) {
  const text = textOf(part);

  return (
    <div className="sk-step__part">
      <div className="sk-step__partbar">
        <span>{partLabel(part)}</span>
        <CopyButton text={text} label="Copy" />
      </div>
      <pre className="sk-step__code">{text}</pre>
      {part.truncated && <p className="sk-step__cut">This text was cut; the record keeps all of it.</p>}
    </div>
  );
}

/** The one line a collapsed step shows, such as "Model turn: called search_logs". */
function summaryOf(step: Step): string {
  const label = KIND_LABELS[step.kind];

  if (step.kind === "tool_result") {
    return `${label}: ${step.tool ?? "tool"}${step.source ? ` (${step.source})` : ""}${step.status === "ok" ? "" : `, ${step.status.replace("_", " ")}`}`;
  }

  const calls = step.parts.filter((p) => p.type === "tool_call").map((p) => p.name);
  if (calls.length > 0) {
    return `${label}: called ${calls.join(", ")}`;
  }

  const text = step.parts.find((p) => p.type === "text")?.text;
  if (text && (step.kind === "model_turn" || step.kind === "note" || step.kind === "summary")) {
    return `${label}: ${firstLine(text)}`;
  }

  return label;
}

function metaOf(step: Step): string {
  const tokens = step.tokens ? `${formatTokens(step.tokens.input)} in, ${formatTokens(step.tokens.output)} out` : "";

  return [formatDuration(step.durationMs), tokens].filter(Boolean).join(" · ");
}

const SUMMARY_CHARS = 80;

function firstLine(text: string): string {
  const line = text.trim().split("\n")[0] ?? "";

  return line.length > SUMMARY_CHARS ? `${line.slice(0, SUMMARY_CHARS)}…` : line;
}

function partLabel(part: StepPart): string {
  switch (part.type) {
    case "tool_call":
      return `Call: ${part.name ?? "tool"}`;
    case "tool_result":
      return part.isError ? "Result (error)" : "Result";
    case "reasoning":
      return "Reasoning";
    case "text":
      return "Text";
  }
}

function textOf(part: StepPart): string {
  switch (part.type) {
    case "tool_call":
      return JSON.stringify(part.input ?? {}, null, 2);
    case "tool_result":
      return part.content ?? "";
    default:
      return part.text ?? "";
  }
}
