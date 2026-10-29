import { useSetFixApplied } from "../../api/fixApplied";
import type { Investigation } from "../../api/investigations";
import { StatusBadge } from "../../components/Badge";
import { Button } from "../../components/Button";
import { formatTime } from "../../format";
import "./FixApplied.css";

/**
 * Whether the fix of a false positive was applied. A responder marks it once
 * they changed the monitor, and the dashboard counts it under noisy alerts
 * fixed. It shows only for an investigation that ended as a false positive.
 */
export function FixApplied({ inv, canAct }: { inv: Investigation; canAct: boolean }) {
  const set = useSetFixApplied(inv.id);

  if (inv.verdict !== "false_positive" || inv.status !== "final") {
    return null;
  }

  const applied = inv.fixAppliedAt !== undefined;

  return (
    <section aria-labelledby="fix-applied-heading" className="sk-fix">
      <h2 id="fix-applied-heading" className="sk-inv__heading">
        Fix
      </h2>
      <div className="sk-fix__row">
        {applied ? <StatusBadge status="success">Fix applied</StatusBadge> : <StatusBadge status="neutral">Fix not applied yet</StatusBadge>}
        {applied && <span>{`Marked ${formatTime(inv.fixAppliedAt)}.`}</span>}
        {canAct && (
          <Button onClick={() => set.mutate(!applied)} disabled={set.isPending}>
            {applied ? "Take back the mark" : "Mark fix as applied"}
          </Button>
        )}
      </div>
      {set.isError && (
        <p role="alert" className="sk-field__error">
          {set.error.message}
        </p>
      )}
    </section>
  );
}
