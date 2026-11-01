import { Alert } from "../../components/Alert";
import { Table } from "../../components/Table";
import { formatCost } from "../../format";
import type { StrategyGate } from "../../api/strategyProposals";
import { GATE_ADVICE } from "./proposalLabels";

const DIGITS = 3;

/** A score difference with its sign, such as "+0.020" or "-0.150". */
function signed(n: number): string {
  return `${n > 0 ? "+" : ""}${n.toFixed(DIGITS)}`;
}

/**
 * How the benchmark gate ended: the result in words, the mean difference with
 * its 95% interval against the drop that is allowed, the score of each
 * scenario with the current version and with the change, and what the gate
 * cost.
 */
export function GateSummary({ gate }: { gate: StrategyGate }) {
  const { title, advice } = GATE_ADVICE[gate.result];
  const tone = gate.result === "passed" ? "success" : "warning";

  return (
    <section aria-label="Benchmark gate" className="sk-proposal__section">
      <Alert tone={tone} title={title}>
        <p>{advice}</p>
        {gate.error && <p>{gate.error}</p>}
      </Alert>
      {gate.scenarios.length > 0 && (
        <>
          <dl className="sk-proposal__facts">
            <div>
              <dt>Mean difference</dt>
              <dd>{signed(gate.mean)}</dd>
            </div>
            <div>
              <dt>95% interval</dt>
              <dd>
                {signed(gate.interval.lower)} to {signed(gate.interval.upper)}
              </dd>
            </div>
            <div>
              <dt>Passes when the lower end is above</dt>
              <dd>{signed(-gate.maxDrop)}</dd>
            </div>
            <div>
              <dt>Repeats of each scenario</dt>
              <dd>{gate.repeats}</dd>
            </div>
            <div>
              <dt>Cost of the gate</dt>
              <dd>
                {formatCost(gate.costMicros)} of {formatCost(gate.maxCostMicros)}
              </dd>
            </div>
          </dl>
          <Table label="Score of each scenario">
            <thead>
              <tr>
                <th scope="col">Scenario</th>
                <th scope="col">Current version</th>
                <th scope="col">Change</th>
                <th scope="col">Difference</th>
              </tr>
            </thead>
            <tbody>
              {gate.scenarios.map((s) => (
                <tr key={s.id}>
                  <th scope="row">{s.id}</th>
                  <td>{s.baseline.toFixed(DIGITS)}</td>
                  <td>{s.proposal.toFixed(DIGITS)}</td>
                  <td>{signed(s.difference)}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        </>
      )}
    </section>
  );
}
