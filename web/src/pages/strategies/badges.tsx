import { Lock, Package, Pencil, type LucideIcon } from "lucide-react";
import type { StrategyLayer, StrategyStatus } from "../../api/strategies";
import { RuleStatusBadge } from "../rules/RuleBadges";

const LAYERS: Record<StrategyLayer, { label: string; Icon: LucideIcon }> = {
  "built-in": { label: "Built in", Icon: Package },
  file: { label: "Strategy file", Icon: Lock },
  runtime: { label: "Added at runtime", Icon: Pencil },
};

/** Where a strategy is defined, as an icon and a word. A built-in strategy and a strategy file cannot be changed here. */
export function StrategyLayerBadge({ layer }: { layer: string }) {
  const { label, Icon } = LAYERS[layer as StrategyLayer] ?? { label: layer, Icon: Pencil };

  return (
    <span className="sk-badge sk-badge--status-neutral">
      <Icon size={16} aria-hidden="true" />
      {label}
    </span>
  );
}

/** Whether a strategy is in effect, with the reason under it when it is not. */
export function StrategyStatusBadge({ status, reason }: { status: string; reason?: string }) {
  return <RuleStatusBadge status={status as StrategyStatus} reason={reason} />;
}
