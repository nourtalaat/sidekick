import type { components } from "../api/schema";
import "./ConfidenceMeter.css";

export type Confidence = components["schemas"]["Confidence"];

const STEPS: Record<Confidence, { label: string; filled: number }> = {
  low: { label: "Low confidence", filled: 1 },
  medium: { label: "Medium confidence", filled: 2 },
  high: { label: "High confidence", filled: 3 },
};

const TOTAL_STEPS = 3;

/** How sure Sidekick is: a meter of three steps and the words. The words carry the meaning; the steps are decoration. */
export function ConfidenceMeter({ confidence }: { confidence: Confidence }) {
  const { label, filled } = STEPS[confidence];

  return (
    <span className="sk-confidence">
      <span className="sk-confidence__steps" aria-hidden="true">
        {Array.from({ length: TOTAL_STEPS }, (_, i) => (
          <span key={i} className={i < filled ? "sk-confidence__step sk-confidence__step--on" : "sk-confidence__step"} />
        ))}
      </span>
      {label}
    </span>
  );
}
