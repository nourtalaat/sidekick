import type { BenchRun } from "../../api/bench";
import { Card } from "../../components/Card";
import { Table } from "../../components/Table";
import { formatShare } from "../dashboard/words";
import { intervalText, metricScoreRows } from "./metricRows";

/** Each share metric of the run with its value and its 95% interval. */
export function ScoresCard({ metrics, intervals }: { metrics: BenchRun["metrics"]; intervals: BenchRun["intervals"] }) {
  return (
    <Card title="Scores">
      <Table label="Scores of this run">
        <thead>
          <tr>
            <th scope="col">Metric</th>
            <th scope="col">Value</th>
            <th scope="col">95% interval</th>
          </tr>
        </thead>
        <tbody>
          {metricScoreRows(metrics, intervals).map((row) => (
            <tr key={row.key}>
              <th scope="row">{row.label}</th>
              <td className="sk-benchrun__num">{formatShare(row.value)}</td>
              <td className="sk-benchrun__num">{intervalText(row.interval)}</td>
            </tr>
          ))}
        </tbody>
      </Table>
    </Card>
  );
}
