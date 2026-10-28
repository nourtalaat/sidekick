import { CircleCheck, CircleStop, Eye, TimerOff, type LucideIcon } from "lucide-react";
import type { components } from "../api/schema";
import "./WatcherBadge.css";

export type WatcherStatus = components["schemas"]["WatcherStatus"];

const STATES: Record<WatcherStatus, { label: string; Icon: LucideIcon; tone: string }> = {
  running: { label: "Checking", Icon: Eye, tone: "checking" },
  recovered: { label: "Back up", Icon: CircleCheck, tone: "recovered" },
  timed_out: { label: "Timed out", Icon: TimerOff, tone: "timedout" },
  stopped: { label: "Stopped", Icon: CircleStop, tone: "stopped" },
};

/** The word for a watcher's state, as the design names it. */
export function watcherLabel(status: WatcherStatus): string {
  return STATES[status].label;
}

/** The tone class suffix of a watcher state, for the colors of its box. */
export function watcherTone(status: WatcherStatus): string {
  return STATES[status].tone;
}

/** A watcher's state as an icon and its word, in the watcher colors. */
export function WatcherBadge({ status }: { status: WatcherStatus }) {
  const { label, Icon, tone } = STATES[status];

  return (
    <span className={`sk-watcher-badge sk-watcher-badge--${tone}`}>
      <Icon size={16} aria-hidden="true" />
      {label}
    </span>
  );
}

/** The icon of a watcher's state. */
export function watcherIcon(status: WatcherStatus): LucideIcon {
  return STATES[status].Icon;
}
