import { useState } from "react";
import type { Watcher } from "../../api/investigations";
import { Button } from "../../components/Button";
import { StatusBadge, type Status } from "../../components/Badge";
import { Table } from "../../components/Table";
import { formatClock, formatDuration, formatTime } from "../../format";
import { endedLine } from "./watcherWords";
import "./WatcherChecks.css";

type Check = Watcher["recentChecks"][number];

/** A check detail with more lines or characters than this gets a way to see all of it. */
const DETAIL_PREVIEW_LINES = 3;
const DETAIL_PREVIEW_CHARS = 200;

const RESULTS: Record<Check["result"], { label: string; status: Status }> = {
  pass: { label: "Passed", status: "success" },
  fail: { label: "Failed", status: "danger" },
  error: { label: "Error", status: "warning" },
};

/**
 * The history of one watcher: how it started and ended, and its last checks,
 * newest first. It sits outside the status region of the watcher box, so a
 * new check is not read aloud. It starts open for a watcher that runs and
 * closed for one that ended; after that the browser keeps the reader's choice,
 * even when the watcher ends. The detail of a check came from the checked
 * service, so it is plain text.
 */
export function WatcherChecks({ watcher }: { watcher: Watcher }) {
  const [openByDefault] = useState(() => watcher.status === "running");
  const ended = endedLine(watcher);
  const rows = watcher.recentChecks;

  return (
    <details className="sk-watcher-checks" open={openByDefault}>
      <summary>
        Checks ({watcher.checks}) <span className="sk-watcher-checks__name">of {watcher.name}</span>
      </summary>
      <ul className="sk-watcher-checks__states">
        <li>Started {formatTime(watcher.startedAt)}</li>
        {ended && <li>{ended}</li>}
      </ul>
      {rows.length === 0 ? (
        <p className="sk-watcher-checks__note">No checks yet.</p>
      ) : (
        <Table label={`Checks of ${watcher.name}`}>
          <thead>
            <tr>
              <th scope="col">Time</th>
              <th scope="col">Result</th>
              <th scope="col">Detail</th>
              <th scope="col">Duration</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((check) => (
              <tr key={check.seq}>
                <td>
                  <time dateTime={check.at}>{formatClock(check.at)}</time>
                </td>
                <td>
                  <StatusBadge status={RESULTS[check.result].status}>{RESULTS[check.result].label}</StatusBadge>
                </td>
                <td>
                  <CheckDetail text={check.detail} />
                </td>
                <td>{formatDuration(check.durationMs)}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {watcher.checks > rows.length && (
        <p className="sk-watcher-checks__note">
          Showing the last {rows.length} of {watcher.checks} checks.
        </p>
      )}
    </details>
  );
}

/** The detail of a check as plain text, cut to a few lines until the reader asks for all of it. */
function CheckDetail({ text }: { text: string | undefined }) {
  const [showAll, setShowAll] = useState(false);

  if (!text) {
    return <span className="sk-watcher-checks__note">No detail</span>;
  }

  const long = isLong(text);

  return (
    <>
      <p className={long && !showAll ? "sk-plain sk-watcher-checks__detail sk-watcher-checks__detail--cut" : "sk-plain sk-watcher-checks__detail"}>{text}</p>
      {long && (
        <Button className="sk-watcher-checks__more" aria-expanded={showAll} onClick={() => setShowAll(!showAll)}>
          {showAll ? "Show less" : "Show all"}
        </Button>
      )}
    </>
  );
}

function isLong(text: string): boolean {
  return text.length > DETAIL_PREVIEW_CHARS || text.split("\n").length > DETAIL_PREVIEW_LINES;
}
