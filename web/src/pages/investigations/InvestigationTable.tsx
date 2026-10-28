import { ThumbsDown, ThumbsUp, type LucideIcon } from "lucide-react";
import { Link } from "react-router";
import type { InvestigationStatus, InvestigationSummary } from "../../api/investigations";
import { StatusBadge, VerdictBadge, type Status } from "../../components/Badge";
import { WatcherBadge } from "../../components/WatcherBadge";
import { formatCost, formatDuration, formatTime } from "../../format";
import "./InvestigationTable.css";

const STATUS_BADGES: Record<InvestigationStatus, { status: Status; label: string; spinning?: boolean }> = {
  queued: { status: "neutral", label: "Queued" },
  running: { status: "info", label: "Investigating", spinning: true },
  final: { status: "neutral", label: "No verdict" },
  failed: { status: "danger", label: "Failed" },
  cancelled: { status: "neutral", label: "Cancelled" },
};

const FEEDBACK: Record<string, { label: string; Icon: LucideIcon } | undefined> = {
  correct: { label: "Correct", Icon: ThumbsUp },
  wrong: { label: "Wrong", Icon: ThumbsDown },
  mixed: { label: "Mixed", Icon: ThumbsUp },
};

/** The investigations as a table, one row each, with the verdict first so the list can be scanned. */
export function InvestigationTable({ rows, compact }: { rows: readonly InvestigationSummary[]; compact: boolean }) {
  return (
    <div className="sk-table-wrap">
      <table className={compact ? "sk-table sk-table--compact" : "sk-table"}>
        <caption className="sk-visually-hidden">Investigations, newest first</caption>
        <thead>
          <tr>
            <th scope="col">Verdict</th>
            <th scope="col">Alert</th>
            <th scope="col">Service</th>
            <th scope="col">Route</th>
            <th scope="col">Started</th>
            <th scope="col">Duration</th>
            <th scope="col">Cost</th>
            <th scope="col">Feedback</th>
            <th scope="col">Watcher</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <InvestigationRow key={row.id} row={row} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function InvestigationRow({ row }: { row: InvestigationSummary }) {
  const feedback = FEEDBACK[row.feedback.summary];

  return (
    <tr>
      <td>
        <VerdictCell row={row} />
      </td>
      <td className="sk-table__title">
        <Link to={`/investigations/${row.id}`}>{row.title || "(no title)"}</Link>
      </td>
      <td>{row.service}</td>
      <td>{row.route}</td>
      <td>{formatTime(row.startedAt ?? row.createdAt)}</td>
      <td>{formatDuration(row.durationMs)}</td>
      <td>{formatCost(row.costMicros)}</td>
      <td>
        {feedback && (
          <span className="sk-table__feedback">
            <feedback.Icon size={16} aria-hidden="true" />
            {feedback.label}
          </span>
        )}
      </td>
      <td>{row.watcher && <WatcherBadge status={row.watcher.status} />}</td>
    </tr>
  );
}

function VerdictCell({ row }: { row: InvestigationSummary }) {
  if (row.verdict) {
    return <VerdictBadge verdict={row.verdict} />;
  }

  const { status, label, spinning } = STATUS_BADGES[row.status];

  return (
    <StatusBadge status={status} spinning={spinning}>
      {label}
    </StatusBadge>
  );
}
