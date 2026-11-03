import type { BenchThreshold } from "../../api/bench";
import { Alert } from "../../components/Alert";
import { failedThresholds, thresholdLimitText, thresholdName, thresholdValueText } from "./thresholds";

/** Names the first threshold a run failed, above the tables. Nothing shows when every threshold passed. */
export function FailedThreshold({ thresholds }: { thresholds: readonly BenchThreshold[] }) {
  const [first, ...others] = failedThresholds(thresholds);

  if (first === undefined) {
    return null;
  }

  return (
    <Alert tone="danger" title={`Failed threshold: ${thresholdName(first.name)}`}>
      <p>{`Value ${thresholdValueText(first)}, limit ${thresholdLimitText(first)}.`}</p>
      {others.length > 0 && <p>{`${others.length} other ${others.length === 1 ? "threshold" : "thresholds"} also failed.`}</p>}
    </Alert>
  );
}
