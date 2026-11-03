import type { Watcher } from "../../api/investigations";
import { formatClock, formatDuration, formatTime } from "../../format";

/** The detail line of a watcher box: what it does now, or how it ended. */
export function watcherDetail(w: Watcher): string {
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

/** How a watcher that ended ended, and when. A running watcher has no such line. */
export function endedLine(w: Watcher): string | undefined {
  switch (w.status) {
    case "running":
      return undefined;
    case "recovered":
    case "stopped":
      return watcherDetail(w);
    case "timed_out":
      return `Timed out at ${formatTime(w.endedAt)} after ${duration(w)}, still down`;
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
