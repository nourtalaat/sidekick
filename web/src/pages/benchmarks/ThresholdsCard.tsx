import type { BenchThreshold } from "../../api/bench";
import { StatusBadge } from "../../components/Badge";
import { Card } from "../../components/Card";
import { Table } from "../../components/Table";
import { thresholdLimitText, thresholdName, thresholdValueText } from "./thresholds";

/** Each threshold the run was checked against, with its limit, its value and whether it passed. */
export function ThresholdsCard({ thresholds }: { thresholds: readonly BenchThreshold[] }) {
  return (
    <Card title="Thresholds">
      {thresholds.length === 0 ? (
        <p>This run set no thresholds.</p>
      ) : (
        <Table label="Thresholds of this run">
          <thead>
            <tr>
              <th scope="col">Threshold</th>
              <th scope="col">Limit</th>
              <th scope="col">Value</th>
              <th scope="col">Result</th>
              <th scope="col">Detail</th>
            </tr>
          </thead>
          <tbody>
            {thresholds.map((t) => (
              <tr key={t.name}>
                <th scope="row">{thresholdName(t.name)}</th>
                <td className="sk-benchrun__num">{thresholdLimitText(t)}</td>
                <td className="sk-benchrun__num">{thresholdValueText(t)}</td>
                <td>
                  <StatusBadge status={t.passed ? "success" : "danger"}>{t.passed ? "Passed" : "Failed"}</StatusBadge>
                </td>
                <td>{t.detail}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Card>
  );
}
