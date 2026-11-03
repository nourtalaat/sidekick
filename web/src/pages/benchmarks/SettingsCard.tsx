import type { BenchComparison } from "../../api/bench";
import { useBenchRun } from "../../api/bench";
import { StatusBadge } from "../../components/Badge";
import { Card } from "../../components/Card";
import { QueryError } from "../../components/QueryError";
import { Table } from "../../components/Table";
import { nameOf } from "./RunsTable";
import { detailOf, settingRows, type SettingCell, type SettingMatch, type SettingRow } from "./runSettings";

const MATCH_WORDS: Record<Exclude<SettingMatch, "differs">, string> = {
  same: "Same",
  pending: "Not compared yet",
  unavailable: "Not compared",
  unchecked: "Not compared",
};

/**
 * What the two runs used, side by side. A row where they differ says
 * "Differs" in words and has its own style. The prompt versions and config
 * hash come from the run pages' data, so they can still be loading or fail
 * without hiding the rest.
 */
export function SettingsCard({ comparison }: { comparison: BenchComparison }) {
  const detailA = useBenchRun(comparison.a.id);
  const detailB = useBenchRun(comparison.b.id);
  const rows = settingRows(comparison.a, comparison.b, detailOf(detailA), detailOf(detailB));
  const failed = [detailA, detailB].filter((query) => query.isError);

  return (
    <Card title="Settings">
      <Table label="Settings of Run A and Run B">
        <thead>
          <tr>
            <th scope="col">Setting</th>
            <th scope="col">
              Run A<span className="sk-table__secondary">{nameOf(comparison.a)}</span>
            </th>
            <th scope="col">
              Run B<span className="sk-table__secondary">{nameOf(comparison.b)}</span>
            </th>
            <th scope="col">Differs</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.name} className={row.match === "differs" ? "sk-compare__row--differs" : undefined}>
              <th scope="row">{row.name}</th>
              <td>
                <Value cell={row.a} mono={row.mono} />
              </td>
              <td>
                <Value cell={row.b} mono={row.mono} />
              </td>
              <td>
                <Match row={row} />
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      {failed.length > 0 && <QueryError error={failed[0]?.error} onRetry={() => failed.forEach((query) => void query.refetch())} />}
    </Card>
  );
}

function Value({ cell, mono }: { cell: SettingCell; mono: boolean }) {
  if (cell.state === "loading") {
    return <>Loading</>;
  }

  if (cell.state === "failed") {
    return <>Not available</>;
  }

  return mono ? <code>{cell.text}</code> : <>{cell.text}</>;
}

function Match({ row }: { row: SettingRow }) {
  if (row.match === "differs") {
    return <StatusBadge status="warning">Differs</StatusBadge>;
  }

  return <>{MATCH_WORDS[row.match]}</>;
}
