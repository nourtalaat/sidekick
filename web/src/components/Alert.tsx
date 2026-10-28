import { Check, CircleX, TriangleAlert, type LucideIcon } from "lucide-react";
import type { ReactNode, Ref } from "react";
import "./Alert.css";

export type AlertTone = "success" | "warning" | "danger";

const ICONS: Record<AlertTone, LucideIcon> = { success: Check, warning: TriangleAlert, danger: CircleX };

interface AlertProps {
  tone: AlertTone;
  title: string;
  children?: ReactNode;
  /** Lets the page move focus here when a save fails, so a screen reader reads it. */
  ref?: Ref<HTMLDivElement>;
}

/**
 * A message above a form or list. Success is a polite status; a warning or
 * danger is an alert that a screen reader reads at once. It has an icon and
 * a title, so color is never the only signal.
 */
export function Alert({ tone, title, children, ref }: AlertProps) {
  const Icon = ICONS[tone];

  return (
    <div ref={ref} role={tone === "success" ? "status" : "alert"} tabIndex={-1} className={`sk-alert sk-alert--${tone}`}>
      <Icon size={20} aria-hidden="true" />
      <div className="sk-alert__body">
        <h2 className="sk-alert__title">{title}</h2>
        {children}
      </div>
    </div>
  );
}
