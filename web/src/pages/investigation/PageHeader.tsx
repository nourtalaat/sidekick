import { CircleCheck, OctagonX } from "lucide-react";
import { useResolve, useStopRun, type Investigation } from "../../api/investigations";
import { Button } from "../../components/Button";
import { CopyButton } from "../../components/CopyButton";
import { formatTime } from "../../format";
import "./PageHeader.css";

interface PageHeaderProps {
  inv: Investigation;
  /** Whether the viewer is a responder or higher, so the action buttons show. */
  canAct: boolean;
  onAskFollowUp(): void;
}

/**
 * The title, the line of facts under it and the actions. Stop and Resolve
 * are for responders and above, and show only when they can apply: Stop
 * while a run goes, Resolve while the group is open.
 */
export function PageHeader({ inv, canAct, onAskFollowUp }: PageHeaderProps) {
  const stop = useStopRun(inv.id);
  const resolve = useResolve(inv.id);

  const running = inv.status === "queued" || inv.status === "running";
  const facts = [inv.service, inv.route, inv.startedAt ? `Started ${formatTime(inv.startedAt)}` : undefined, countOf(inv.eventCount)].filter(Boolean);
  const failure = stop.error ?? resolve.error;

  return (
    <header className="sk-inv-header">
      <div className="sk-inv-header__top">
        <h1 className="sk-page__title sk-inv-header__title">{inv.title || "Investigation"}</h1>
        <div className="sk-inv-header__actions">
          {canAct && <Button onClick={onAskFollowUp}>Ask a follow-up</Button>}
          <CopyButton text={window.location.href} label="Copy link" />
          {canAct && running && inv.cancelRequestedAt === undefined && (
            <Button variant="destructive" onClick={() => stop.mutate()} disabled={stop.isPending}>
              <OctagonX size={16} aria-hidden="true" />
              Stop
            </Button>
          )}
          {canAct && inv.groupStatus === "open" && (
            <Button onClick={() => resolve.mutate()} disabled={resolve.isPending}>
              <CircleCheck size={16} aria-hidden="true" />
              Resolve
            </Button>
          )}
        </div>
      </div>
      <p className="sk-inv-header__facts">{facts.join(" · ")}</p>
      {running && inv.cancelRequestedAt !== undefined && <p role="status">Stop asked for. The run ends at its next step.</p>}
      {inv.groupStatus === "closed" && inv.groupClosedReason === "resolved" && <p className="sk-inv-header__facts">Marked as resolved.</p>}
      {failure && (
        <p role="alert" className="sk-field__error">
          {failure.message}
        </p>
      )}
    </header>
  );
}

function countOf(events: number): string | undefined {
  return events > 1 ? `${events} alerts` : undefined;
}
