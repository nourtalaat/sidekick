import { BellOff, Check, CircleHelp, CircleX, Info, Link, LoaderCircle, Pin, Repeat, Siren, TriangleAlert, Clock, type LucideIcon } from "lucide-react";
import "./Badge.css";

/** The verdicts of a report, as the API names them. */
export type Verdict = "true_positive" | "false_positive" | "inconclusive" | "duplicate" | "known_issue" | "flapping";

export type Status = "success" | "info" | "warning" | "danger" | "neutral";

const VERDICTS: Record<Verdict, { label: string; Icon: LucideIcon; tone: string }> = {
  true_positive: { label: "Real issue", Icon: Siren, tone: "real" },
  false_positive: { label: "False alarm", Icon: BellOff, tone: "false" },
  inconclusive: { label: "Not sure yet", Icon: CircleHelp, tone: "unsure" },
  duplicate: { label: "Duplicate", Icon: Link, tone: "duplicate" },
  known_issue: { label: "Known issue", Icon: Pin, tone: "known" },
  flapping: { label: "Flapping", Icon: Repeat, tone: "flapping" },
};

const STATUS_ICONS: Record<Status, LucideIcon> = {
  success: Check,
  info: Info,
  warning: TriangleAlert,
  danger: CircleX,
  neutral: Clock,
};

/** A verdict as an icon and its label. The label is text, so a screen reader reads it. */
export function VerdictBadge({ verdict }: { verdict: Verdict }) {
  const { label, Icon, tone } = VERDICTS[verdict];

  return (
    <span className={`sk-badge sk-badge--verdict-${tone}`}>
      <Icon size={16} aria-hidden="true" />
      {label}
    </span>
  );
}

/** A state of the system, such as "Queued" or "Failed", as an icon and its label. */
export function StatusBadge({ status, spinning = false, children }: { status: Status; spinning?: boolean; children: string }) {
  const Icon = spinning ? LoaderCircle : STATUS_ICONS[status];

  return (
    <span className={`sk-badge sk-badge--status-${status}`}>
      <Icon size={16} aria-hidden="true" className={spinning ? "sk-spin" : undefined} />
      {children}
    </span>
  );
}
