import { Link } from "react-router";
import type { BenchRun } from "../../api/bench";
import { StatusBadge } from "../../components/Badge";
import { LinkButton } from "../../components/Button";
import { Card } from "../../components/Card";
import { CopyButton } from "../../components/CopyButton";
import { formatTime } from "../../format";
import { Fact } from "./Fact";
import { nameOf, startedByOf } from "./RunsTable";

/** The title, the result and the facts a run was made with, and the links to the list and to a comparison. */
export function RunHeader({ run }: { run: BenchRun }) {
  const roles = Object.entries(run.connectors).sort(([a], [b]) => a.localeCompare(b));
  const strategies = Object.entries(run.strategies).sort(([a], [b]) => a.localeCompare(b));

  return (
    <>
      <div className="sk-benchrun__title">
        <h1 className="sk-page__title">{nameOf(run)}</h1>
        <StatusBadge status={run.status === "passed" ? "success" : "danger"}>{run.status === "passed" ? "Passed" : "Failed"}</StatusBadge>
      </div>
      <div className="sk-page__actions">
        <LinkButton to={`/benchmarks/compare?a=${encodeURIComponent(run.id)}`}>Compare with another run</LinkButton>
        <Link to="/benchmarks">All benchmark runs</Link>
      </div>
      <Card title="About this run">
        <dl className="sk-benchrun__facts">
          <Fact name="Started">
            <time dateTime={run.startedAt}>{formatTime(run.startedAt)}</time>
          </Fact>
          <Fact name="Finished">
            <time dateTime={run.finishedAt}>{formatTime(run.finishedAt)}</time>
          </Fact>
          <Fact name="Sidekick version">{run.sidekickVersion}</Fact>
          <Fact name="Git commit">
            <code className="sk-benchrun__mono">{run.gitSha}</code>
          </Fact>
          <Fact name="Started by">{startedByOf(run.triggeredBy)}</Fact>
          <Fact name="Scenario set">{`${run.scenarioSet}, ${run.repeats} ${run.repeats === 1 ? "repeat" : "repeats"}`}</Fact>
          <Fact name="Models">
            {roles.length === 0
              ? "none"
              : roles.map(([role, c]) => (
                  <span key={role} className="sk-table__line">
                    {`${role}: ${c.model ?? "no model"} through ${c.connector}${c.provider ? ` (${c.provider})` : ""}`}
                  </span>
                ))}
          </Fact>
          <Fact name="Strategies">
            {strategies.length === 0
              ? "none"
              : strategies.map(([name, version]) => (
                  <span key={name} className="sk-table__line">{`${name}, version ${version}`}</span>
                ))}
          </Fact>
          <Fact name="Prompt version">{run.promptVersion}</Fact>
          <Fact name="Judge prompt version">{run.judgePromptVersion}</Fact>
          <Fact name="Config hash">
            <code className="sk-benchrun__mono">{run.configHash}</code> <CopyButton text={run.configHash} label="Copy config hash" />
          </Fact>
        </dl>
      </Card>
    </>
  );
}
