import { Link, useSearchParams } from "react-router";
import { useScenarioList, type ScenarioStatus } from "../../api/scenarios";
import { StatusBadge } from "../../components/Badge";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { Select } from "../../components/Select";
import { Table } from "../../components/Table";
import { formatTime } from "../rules/format";
import "./Scenarios.css";

const STATUS_OPTIONS = [
  { value: "", label: "All" },
  { value: "draft", label: "Drafts" },
  { value: "reviewed", label: "Reviewed" },
] as const;

/** The status filter of the address, or undefined for all. */
function statusOf(value: string | null): ScenarioStatus | undefined {
  return value === "draft" || value === "reviewed" ? value : undefined;
}

/** The scenarios, most recently changed first, with a filter by status. */
export function ScenarioList() {
  const [params, setParams] = useSearchParams();
  const status = statusOf(params.get("status"));
  const list = useScenarioList(status);

  const scenarios = list.data?.pages.flatMap((page) => page.scenarios) ?? [];

  function choose(value: string) {
    const next = new URLSearchParams(params);
    if (value === "") {
      next.delete("status");
    } else {
      next.set("status", value);
    }

    setParams(next);
  }

  return (
    <section aria-labelledby="scenarios-heading" className="sk-scenarios">
      <h2 id="scenarios-heading" className="sk-scenarios__title">
        Benchmark scenarios
      </h2>
      <p>
        Confirmed investigations exported as scenarios. A draft has to be checked for sensitive data and marked as reviewed before it can be run or downloaded. Export one with the
        Export as scenario button on a confirmed investigation.
      </p>
      <div className="sk-scenarios__filter">
        <Select label="Show" value={status ?? ""} options={STATUS_OPTIONS} onChange={(e) => choose(e.target.value)} />
      </div>
      {list.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {list.isError && <QueryError error={list.error} onRetry={() => void list.refetch()} />}
      {list.isSuccess && scenarios.length === 0 && (
        <EmptyState title={status ? "No scenarios with this status" : "No scenarios yet"}>
          {status ? "Show all scenarios to see the rest." : "Open a confirmed investigation and choose Export as scenario."}
        </EmptyState>
      )}
      {list.isSuccess && scenarios.length > 0 && (
        <>
          <Table label="Scenarios">
            <thead>
              <tr>
                <th scope="col">Status</th>
                <th scope="col">Scenario</th>
                <th scope="col">Investigation</th>
                <th scope="col">Changed</th>
              </tr>
            </thead>
            <tbody>
              {scenarios.map((scenario) => (
                <tr key={scenario.id}>
                  <td>
                    <StatusBadge status={scenario.status === "reviewed" ? "success" : "neutral"}>{scenario.status === "reviewed" ? "Reviewed" : "Draft"}</StatusBadge>
                  </td>
                  <td>
                    <Link to={`/review?tab=scenarios&id=${encodeURIComponent(scenario.id)}`}>{scenario.title}</Link>
                    <div className="sk-scenarios__id">
                      <code>{scenario.id}</code>
                    </div>
                  </td>
                  <td>{scenario.investigationId ? <Link to={`/investigations/${scenario.investigationId}`}>Open investigation</Link> : "Deleted"}</td>
                  <td>
                    <time dateTime={scenario.updatedAt}>{formatTime(scenario.updatedAt)}</time>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
          {list.hasNextPage && (
            <div>
              <Button onClick={() => void list.fetchNextPage()} disabled={list.isFetchingNextPage}>
                {list.isFetchingNextPage ? "Loading" : "Show more"}
              </Button>
            </div>
          )}
        </>
      )}
    </section>
  );
}
