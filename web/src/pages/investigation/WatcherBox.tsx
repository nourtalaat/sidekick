import type { Watcher } from "../../api/investigations";
import { Button } from "../../components/Button";
import { watcherIcon, watcherLabel, watcherTone } from "../../components/WatcherBadge";
import { formatClock, formatDuration, formatTime } from "../../format";
import "./WatcherBox.css";

interface WatcherBoxProps {
  watchers: readonly Watcher[];
  /** Whether the viewer may stop watchers. */
  canStop: boolean;
  onStop(watcher: Watcher): void;
  /** The ID of the watcher whose stop is being sent. */
  stopping?: string;
}

/**
 * The state of the watchers of the group, one box each. The box is a status
 * region, so a screen reader announces a change; it does not animate. What a
 * watcher found in its checks came from the checked service, so it is shown
 * as plain text.
 */
export function WatcherBox({ watchers, canStop, onStop, stopping }: WatcherBoxProps) {
  if (watchers.length === 0) {
    return null;
  }

  return (
    <div role="status" className="sk-watchers" aria-label="Watchers">
      {watchers.map((watcher) => {
        const Icon = watcherIcon(watcher.status);

        return (
          <div key={watcher.id} className={`sk-watcher sk-watcher--${watcherTone(watcher.status)}`}>
            <Icon size={20} aria-hidden="true" className="sk-watcher__icon" />
            <div className="sk-watcher__text">
              <p className="sk-watcher__title">
                {watcherLabel(watcher.status)}: {watcher.name}
              </p>
              <p className="sk-watcher__detail">{detailOf(watcher)}</p>
            </div>
            {canStop && watcher.status === "running" && (
              <Button onClick={() => onStop(watcher)} disabled={stopping === watcher.id}>
                Stop watching
              </Button>
            )}
          </div>
        );
      })}
    </div>
  );
}

/** The detail line of a watcher: what it does now, or how it ended. */
function detailOf(w: Watcher): string {
  switch (w.status) {
    case "running":
      return [
        `${w.passesInARow} of ${w.passesToRecover} passes`,
        `every ${w.intervalSeconds}s`,
        w.lastCheckAt ? `last check ${formatClock(w.lastCheckAt)}` : "no check yet",
        w.lastResult,
      ]
        .filter(Boolean)
        .join(" · ");
    case "recovered":
      return `Back up at ${formatTime(w.endedAt)} after ${duration(w)}`;
    case "timed_out":
      return `Stopped after ${duration(w)}, still down`;
    case "stopped":
      return `${stoppedBy(w)} at ${formatTime(w.endedAt)} after ${duration(w)}`;
  }
}

function duration(w: Watcher): string {
  if (!w.endedAt) {
    return "";
  }

  return formatDuration(new Date(w.endedAt).getTime() - new Date(w.startedAt).getTime());
}

function stoppedBy(w: Watcher): string {
  if (w.stopReason === "group_resolved") {
    return "Stopped because the group was resolved";
  }

  if (w.stopReason === "config_changed") {
    return "Stopped because the config changed";
  }

  return `Stopped by ${w.stoppedBy?.name ?? (w.stoppedBy?.slackUserId ? "a Slack user" : "a responder")}`;
}
