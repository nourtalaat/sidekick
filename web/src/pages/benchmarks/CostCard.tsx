import type { BenchRun } from "../../api/bench";
import { Card } from "../../components/Card";
import { formatCost, formatDuration, MICROS_PER_DOLLAR } from "../../format";
import { formatCount } from "../dashboard/words";
import { Fact } from "./Fact";

/** What the run cost and how long it took, from its totals. The judge's own cost is counted apart. */
export function CostCard({ metrics }: { metrics: BenchRun["metrics"] }) {
  return (
    <Card title="Cost and time">
      <dl className="sk-benchrun__facts">
        <Fact name="Total model cost">{formatCost(metrics.costMicros)}</Fact>
        <Fact name="Judge cost">{formatCost(metrics.judgeCostMicros)}</Fact>
        <Fact name="Input tokens">{formatCount(metrics.inputTokens)}</Fact>
        <Fact name="Output tokens">{formatCount(metrics.outputTokens)}</Fact>
        <Fact name="Tool calls">{formatCount(metrics.toolCalls)}</Fact>
        <Fact name="Mean cost per run">{metrics.meanCostPerRunUsd === undefined ? "none yet" : formatCost(metrics.meanCostPerRunUsd * MICROS_PER_DOLLAR)}</Fact>
        <Fact name="Mean time per run">{metrics.meanDurationMs === undefined ? "none yet" : formatDuration(metrics.meanDurationMs)}</Fact>
        <Fact name="Judge errors">{formatCount(metrics.judgeErrors)}</Fact>
      </dl>
    </Card>
  );
}
