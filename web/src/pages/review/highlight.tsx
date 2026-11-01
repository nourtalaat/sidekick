import type { ReactNode } from "react";
import type { ScenarioCandidate } from "../../api/scenarios";

/** The placeholders the cleaner writes, and the ones redaction wrote when the data was stored. */
const PLACEHOLDER = /\b(?:host|user|account|ip)-\d+\b|\[REDACTED:[^\]]*\]/g;

interface Span {
  start: number;
  end: number;
  kind: "placeholder" | "check";
  reason?: string;
}

/**
 * The text with the placeholders marked as replaced values and the places
 * the cleaner could not classify marked as places to check. A candidate's
 * offsets are bytes of UTF-8, as the server counts them.
 */
export function Highlighted({ text, candidates = [] }: { text: string; candidates?: readonly ScenarioCandidate[] }) {
  const spans = [...placeholderSpans(text), ...candidateSpans(text, candidates)].sort((a, b) => a.start - b.start);

  const out: ReactNode[] = [];
  let at = 0;

  for (const span of spans) {
    if (span.start < at) {
      continue;
    }

    out.push(text.slice(at, span.start));
    out.push(
      <mark key={span.start} className={span.kind === "check" ? "sk-mark sk-mark--check" : "sk-mark"} title={span.reason}>
        {text.slice(span.start, span.end)}
      </mark>,
    );
    at = span.end;
  }

  out.push(text.slice(at));

  return <>{out}</>;
}

function placeholderSpans(text: string): Span[] {
  return Array.from(text.matchAll(PLACEHOLDER), (m) => ({ start: m.index, end: m.index + m[0].length, kind: "placeholder" as const }));
}

function candidateSpans(text: string, candidates: readonly ScenarioCandidate[]): Span[] {
  const bytes = new TextEncoder().encode(text);
  const decoder = new TextDecoder();

  return candidates
    .filter((c) => c.start >= 0 && c.end > c.start && c.end <= bytes.length)
    .map((c) => ({
      start: decoder.decode(bytes.slice(0, c.start)).length,
      end: decoder.decode(bytes.slice(0, c.end)).length,
      kind: "check" as const,
      reason: c.reason,
    }));
}
