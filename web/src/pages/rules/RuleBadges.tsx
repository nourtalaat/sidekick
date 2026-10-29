import { Lock, Pencil } from "lucide-react";
import { StatusBadge, type Status } from "../../components/Badge";
import type { RuleStatus } from "../../api/rules";

const STATUS: Record<RuleStatus, { status: Status; label: string }> = {
  active: { status: "success", label: "Active" },
  shadowed: { status: "warning", label: "Shadowed" },
  invalid: { status: "danger", label: "Invalid" },
  disabled: { status: "neutral", label: "Turned off" },
};

/** The status of a rule as an icon and a word, with the reason under it when the rule is not in effect. */
export function RuleStatusBadge({ status, reason }: { status: RuleStatus; reason?: string }) {
  const { status: tone, label } = STATUS[status];

  return (
    <>
      <StatusBadge status={tone}>{label}</StatusBadge>
      {reason && <span className="sk-table__secondary">{reason}</span>}
    </>
  );
}

/** Where a rule is defined: the config file, which the UI cannot change, or runtime. */
export function RuleLayerBadge({ layer }: { layer: "file" | "runtime" }) {
  const Icon = layer === "file" ? Lock : Pencil;

  return (
    <span className="sk-badge sk-badge--status-neutral">
      <Icon size={16} aria-hidden="true" />
      {layer === "file" ? "Config file" : "Added at runtime"}
    </span>
  );
}
