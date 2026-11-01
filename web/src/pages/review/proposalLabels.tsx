import { StatusBadge, type Status } from "../../components/Badge";
import type { StrategyGateResult, StrategyProposalStatus } from "../../api/strategyProposals";

const STATUS: Record<StrategyProposalStatus, { tone: Status; label: string; spinning?: boolean }> = {
  drafting: { tone: "neutral", label: "Being drafted", spinning: true },
  draft_failed: { tone: "danger", label: "Draft failed" },
  pending: { tone: "neutral", label: "Waiting for the gate" },
  benchmarking: { tone: "info", label: "Gate running", spinning: true },
  passed: { tone: "success", label: "Passed the gate" },
  failed: { tone: "danger", label: "Did not pass" },
  approved: { tone: "success", label: "Approved" },
  applied: { tone: "success", label: "Applied" },
  rejected: { tone: "neutral", label: "Rejected" },
};

/** The status of a proposal as an icon and a word. */
export function ProposalStatusBadge({ status }: { status: StrategyProposalStatus }) {
  const { tone, label, spinning } = STATUS[status];

  return (
    <StatusBadge status={tone} spinning={spinning}>
      {label}
    </StatusBadge>
  );
}

/** What each result of the gate means, and what to do about it. */
export const GATE_ADVICE: Record<StrategyGateResult, { title: string; advice: string }> = {
  passed: {
    title: "The benchmark shows the change is not clearly worse",
    advice: "The lower end of the interval is above the allowed drop. A person still decides whether the change is a good one.",
  },
  failed: {
    title: "The change scores lower than the current version",
    advice: "On average the scenarios score lower by more than the allowed drop. Edit the change, or reject it.",
  },
  not_enough_evidence: {
    title: "The benchmark cannot tell",
    advice:
      "The change looks no worse on average, but the interval is too wide to be sure, or too few scenarios cover the strategy. Run more repeats (bench.gate.minRepeats) or add scenarios, then save the proposal again to run the gate again.",
  },
  no_benchmark_coverage: {
    title: "No benchmark scenario covers this strategy",
    advice: "Export confirmed investigations of this strategy as benchmark scenarios, then save the proposal again to run the gate again.",
  },
  over_budget: {
    title: "The gate stopped at its cost cap",
    advice: "A run would have cost more than bench.gate.maxCostUSD. Raise the cap or cover the strategy with fewer scenarios, then save the proposal again.",
  },
  error: {
    title: "The gate could not finish",
    advice: "Save the proposal again to run the gate again. If it keeps failing, check the logs of Sidekick.",
  },
};
