import { ChevronDown, ChevronRight } from "lucide-react";
import { useId, useMemo, useState } from "react";
import type { BenchResult } from "../../api/bench";
import { StatusBadge } from "../../components/Badge";
import { Card } from "../../components/Card";
import { Table } from "../../components/Table";
import { formatCost, formatDuration, formatTokens } from "../../format";
import { VERDICT_OPTIONS } from "../investigations/filters";
import { formatShare } from "../dashboard/words";
import { groupScenarios, type ScenarioGroup, type ScenarioState } from "./scenarios";

const STATES: Record<ScenarioState, { label: string; tone: "danger" | "warning" | "success" }> = {
  wrong: { label: "Wrong", tone: "danger" },
  partly: { label: "Partly wrong", tone: "warning" },
  right: { label: "Correct", tone: "success" },
};

/**
 * One row for each scenario of the run, wrong ones first. A row opens to the
 * result of each repeat. Everything from the run is plain text.
 */
export function ScenariosCard({ results }: { results: readonly BenchResult[] }) {
  const groups = useMemo(() => groupScenarios(results), [results]);

  return (
    <Card title="Scenarios">
      {groups.length === 0 ? (
        <p>No results are stored for this run.</p>
      ) : (
        <Table label="Scenarios of this run">
          <thead>
            <tr>
              <th scope="col">Scenario</th>
              <th scope="col">Result</th>
              <th scope="col">Expected verdict</th>
              <th scope="col">Correct</th>
              <th scope="col">Verdicts given</th>
              <th scope="col">Mean root cause score</th>
              <th scope="col">Mean cost</th>
              <th scope="col">Mean time</th>
            </tr>
          </thead>
          {groups.map((group) => (
            <ScenarioRows key={group.scenarioId} group={group} />
          ))}
        </Table>
      )}
    </Card>
  );
}

function ScenarioRows({ group }: { group: ScenarioGroup }) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const Chevron = open ? ChevronDown : ChevronRight;
  const { label, tone } = STATES[group.state];

  return (
    <tbody>
      <tr>
        <th scope="row">
          <button type="button" className="sk-benchrun__toggle" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen(!open)}>
            <Chevron size={16} aria-hidden="true" />
            {group.scenarioId}
          </button>
        </th>
        <td>
          <StatusBadge status={tone}>{label}</StatusBadge>
        </td>
        <td>{verdictText(group.expectedVerdict)}</td>
        <td className="sk-benchrun__num">{`${group.correct} of ${group.total}`}</td>
        <td>{group.verdicts.map((v) => `${verdictText(v.verdict)} (${v.count})`).join(", ")}</td>
        <td className="sk-benchrun__num">{group.meanRootCauseScore === undefined ? "Not graded" : formatShare(group.meanRootCauseScore)}</td>
        <td className="sk-benchrun__num">{formatCost(group.meanCostMicros)}</td>
        <td className="sk-benchrun__num">{formatDuration(group.meanDurationMs)}</td>
      </tr>
      {open && (
        <tr>
          <td id={panelId} colSpan={8}>
            <RepeatsTable group={group} />
          </td>
        </tr>
      )}
    </tbody>
  );
}

function RepeatsTable({ group }: { group: ScenarioGroup }) {
  return (
    <table className="sk-table sk-benchrun__repeats" aria-label={`Repeats of ${group.scenarioId}`}>
      <thead>
        <tr>
          <th scope="col">Repeat</th>
          <th scope="col">Verdict given</th>
          <th scope="col">Result</th>
          <th scope="col">Root cause score</th>
          <th scope="col">Required items met</th>
          <th scope="col">Evidence score</th>
          <th scope="col">Proposals correct</th>
          <th scope="col">Cost</th>
          <th scope="col">Time</th>
          <th scope="col">Tokens</th>
          <th scope="col">Tool calls</th>
        </tr>
      </thead>
      <tbody>
        {group.repeats.map((r) => (
          <tr key={r.repeat}>
            <th scope="row">{r.repeat}</th>
            <td>{verdictText(r.verdict)}</td>
            <td>
              <StatusBadge status={r.verdictCorrect ? "success" : "danger"}>{r.verdictCorrect ? "Correct" : "Wrong"}</StatusBadge>
            </td>
            <td className="sk-benchrun__num">{r.rootCauseScore === undefined ? "Not graded" : formatShare(r.rootCauseScore)}</td>
            <td>{r.requiredMet === undefined ? "Not graded" : r.requiredMet ? "Met" : "Not met"}</td>
            <td className="sk-benchrun__num">{r.evidenceScore === undefined ? "Not scored" : formatShare(r.evidenceScore)}</td>
            <td>{r.proposalCorrect === undefined ? "Not scored" : r.proposalCorrect ? "Correct" : "Wrong"}</td>
            <td className="sk-benchrun__num">{formatCost(r.costMicros)}</td>
            <td className="sk-benchrun__num">{formatDuration(r.durationMs)}</td>
            <td className="sk-benchrun__num">{`${formatTokens(r.inputTokens)} in, ${formatTokens(r.outputTokens)} out`}</td>
            <td className="sk-benchrun__num">{r.toolCalls}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** A verdict as the badges word it; one this page does not know shows as the API sent it. */
function verdictText(verdict: string | undefined): string {
  return verdict === undefined ? "No verdict" : (VERDICT_OPTIONS.find((o) => o.value === verdict)?.label ?? verdict);
}
