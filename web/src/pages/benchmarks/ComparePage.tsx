import { Link, useSearchParams } from "react-router";
import { BENCH_COMPARE_CHOICES, useBenchComparison, useBenchRuns, type BenchRunSummary } from "../../api/bench";
import { Button } from "../../components/Button";
import { Card } from "../../components/Card";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { Select } from "../../components/Select";
import { MetricsTable, ScenariosTable } from "./ComparisonTables";
import { nameOf } from "./RunsTable";
import { isRunId } from "./runId";
import { SettingsCard } from "./SettingsCard";
import "./Compare.css";

const NONE = "";

interface Choice {
  a: string | undefined;
  b: string | undefined;
}

/**
 * Two benchmark runs side by side: what each used, the change of each
 * metric, and each scenario. The runs are in the address (a and b, as on the
 * dashboard), so a comparison can be shared. Every role from viewer up sees
 * it.
 */
export function ComparePage() {
  const [params, setParams] = useSearchParams();
  const choice: Choice = { a: runIdOf(params.get("a")), b: runIdOf(params.get("b")) };
  const runs = useBenchRuns(BENCH_COMPARE_CHOICES);
  const comparison = useBenchComparison(choice.a, choice.b);

  const choose = (next: Choice) => setParams(paramsOf(next), { replace: true });
  const list = runs.data?.runs ?? [];
  const noRuns = runs.isSuccess && list.length === 0 && !choice.a && !choice.b;

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Compare benchmark runs</h1>
      <p>
        <Link to="/benchmarks">All benchmark runs</Link>
      </p>
      {noRuns ? (
        <EmptyState title="No benchmark runs yet">Runs appear here when sidekick bench run stores its results with --db.</EmptyState>
      ) : (
        <div className="sk-compare__choose">
          <RunMenu label="Run A" value={choice.a} runs={list} onChange={(id) => choose({ ...choice, a: id })} />
          <RunMenu label="Run B" value={choice.b} runs={list} onChange={(id) => choose({ ...choice, b: id })} />
          <Button disabled={!choice.a && !choice.b} onClick={() => choose({ a: choice.b, b: choice.a })}>
            Swap runs
          </Button>
        </div>
      )}
      {runs.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {runs.isError && <QueryError error={runs.error} onRetry={() => void runs.refetch()} />}
      {(!choice.a || !choice.b) && !noRuns && <p>{promptFor(choice)}</p>}
      {comparison.isPending && comparison.fetchStatus !== "idle" && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {comparison.isError && <QueryError error={comparison.error} onRetry={() => void comparison.refetch()} />}
      {comparison.isSuccess && (
        <>
          <SettingsCard comparison={comparison.data} />
          <Card title="Scores">
            <MetricsTable comparison={comparison.data} />
          </Card>
          <Card title="Scenarios">
            <ScenariosTable comparison={comparison.data} />
          </Card>
        </>
      )}
    </div>
  );
}

function RunMenu({ label, value, runs, onChange }: { label: string; value: string | undefined; runs: BenchRunSummary[]; onChange(id: string | undefined): void }) {
  const listed = runs.map((run) => ({ value: run.id, label: nameOf(run) }));
  const outside = value !== undefined && !runs.some((run) => run.id === value) ? [{ value, label: value }] : [];

  return <Select label={label} value={value ?? NONE} options={[{ value: NONE, label: "Choose a run" }, ...outside, ...listed]} onChange={(event) => onChange(event.target.value || undefined)} />;
}

function promptFor(choice: Choice): string {
  if (!choice.a && !choice.b) {
    return "Choose two runs to compare them.";
  }

  return choice.a ? "Choose Run B to compare it with Run A." : "Choose Run A to compare it with Run B.";
}

/** The run ID in an address parameter; a value that is not an ID counts as no run, so every link opens. */
function runIdOf(value: string | null): string | undefined {
  return value && isRunId(value) ? value : undefined;
}

function paramsOf(choice: Choice): URLSearchParams {
  const params = new URLSearchParams();

  if (choice.a) {
    params.set("a", choice.a);
  }

  if (choice.b) {
    params.set("b", choice.b);
  }

  return params;
}
