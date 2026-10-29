import investigatingEyes from "../../../../assets/states/investigating.svg";
import type { Investigation } from "../../api/investigations";
import { StatusBadge, VerdictBadge } from "../../components/Badge";
import { ConfidenceMeter } from "../../components/ConfidenceMeter";
import { verdictTarget } from "../../api/feedback";
import { PartFeedback } from "./PartFeedback";
import "./VerdictPanel.css";

const VERDICT_LABELS: Record<string, string> = {
  true_positive: "Real issue",
  false_positive: "False alarm",
  inconclusive: "Not sure yet",
  duplicate: "Duplicate",
  known_issue: "Known issue",
  flapping: "Flapping",
};

/**
 * The first thing on the page: what it is and how sure Sidekick is. The
 * reason came from the model, so it is shown as plain text. While a run
 * goes, it says so, and the step list below shows the progress.
 */
export function VerdictPanel({ inv, steps }: { inv: Investigation; steps: number }) {
  const report = inv.report;

  if (inv.status === "queued" || inv.status === "running") {
    return (
      <section className="sk-verdict" aria-labelledby="verdict-heading">
        <h2 id="verdict-heading" className="sk-visually-hidden">
          Verdict
        </h2>
        <div className="sk-verdict__running">
          <img src={investigatingEyes} alt="" width={24} height={24} />
          <strong>{inv.status === "queued" ? "Waiting to start" : "Investigating"}</strong>
          <span className="sk-verdict__muted">{steps === 1 ? "1 step so far" : `${steps} steps so far`}</span>
        </div>
        {report?.verdict && <p className="sk-verdict__previous">Last verdict: {VERDICT_LABELS[report.verdict]}</p>}
      </section>
    );
  }

  if (!report || !inv.verdict) {
    return (
      <section className="sk-verdict" aria-labelledby="verdict-heading">
        <h2 id="verdict-heading" className="sk-visually-hidden">
          Verdict
        </h2>
        <StatusBadge status={inv.status === "failed" ? "danger" : "neutral"}>{inv.status === "failed" ? "Failed" : "No verdict"}</StatusBadge>
        {report?.stopDetail && <p className="sk-verdict__reason">{report.stopDetail}</p>}
      </section>
    );
  }

  return (
    <section className="sk-verdict" aria-labelledby="verdict-heading">
      <h2 id="verdict-heading" className="sk-visually-hidden">
        Verdict
      </h2>
      <div className="sk-verdict__line">
        <VerdictBadge verdict={inv.verdict} />
        {inv.confidence && inv.verdict !== "inconclusive" && <ConfidenceMeter confidence={inv.confidence} />}
      </div>
      <p className="sk-verdict__reason">{report.reason}</p>
      {report.stopReason === "triage" && <p className="sk-verdict__muted">Triage only: no full investigation ran.</p>}
      {inv.confirmation && (
        <p className="sk-verdict__muted">People confirmed this verdict: {VERDICT_LABELS[inv.confirmation.verdict]}.</p>
      )}
      <PartFeedback target={verdictTarget(inv.run)} label="the verdict" />
    </section>
  );
}
