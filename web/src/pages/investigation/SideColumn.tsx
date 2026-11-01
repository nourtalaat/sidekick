import { Check, CircleX, Clock, TriangleAlert, type LucideIcon } from "lucide-react";
import { Link } from "react-router";
import type { Investigation } from "../../api/investigations";
import { Card } from "../../components/Card";
import { formatCost, formatDuration, formatTokens } from "../../format";
import "./SideColumn.css";

type SourceStatus = NonNullable<Investigation["report"]>["checked"][number]["status"];

const SOURCE_STATUSES: Record<SourceStatus, { label: string; Icon: LucideIcon; tone: string }> = {
  ok: { label: "Checked", Icon: Check, tone: "success" },
  no_data: { label: "No data", Icon: Clock, tone: "neutral" },
  failed: { label: "Failed", Icon: CircleX, tone: "danger" },
  unavailable: { label: "Unavailable", Icon: TriangleAlert, tone: "warning" },
};

/** What was checked, and what the run cost, took and used. Below 1024px it moves under the report. */
export function SideColumn({ inv }: { inv: Investigation }) {
  const checked = inv.report?.checked ?? [];

  return (
    <aside className="sk-side" aria-label="About this investigation">
      <Card title="Checked">
        {checked.length === 0 ? (
          <p className="sk-side__none">Nothing checked yet.</p>
        ) : (
          <ul className="sk-side__sources">
            {checked.map((c) => {
              const { label, Icon, tone } = SOURCE_STATUSES[c.status];

              return (
                <li key={c.source} className="sk-side__source">
                  <span className={`sk-side__status sk-side__status--${tone}`}>
                    <Icon size={16} aria-hidden="true" />
                    {label}
                  </span>
                  <span className="sk-side__name">{c.source}</span>
                  {c.error && <span className="sk-side__error">{c.error}</span>}
                </li>
              );
            })}
          </ul>
        )}
      </Card>
      <Card title="This run">
        <dl className="sk-side__facts">
          <Fact name="Cost" value={formatCost(inv.costMicros)} />
          <Fact name="Time" value={formatDuration(inv.durationMs)} />
          <Fact name="Tokens" value={`${formatTokens(inv.inputTokens)} in, ${formatTokens(inv.outputTokens)} out`} />
          <Fact name="Tool calls" value={String(inv.toolCalls)} />
          <Fact name="Model" value={inv.model} />
          <Fact name="Route" value={inv.route} />
          <StrategyFact name={inv.strategy} version={inv.strategyVersion} />
          <Fact name="Run" value={`${inv.run}`} />
        </dl>
      </Card>
    </aside>
  );
}

/** The strategy and version the investigation used; a runtime version links to its text. */
function StrategyFact({ name, version }: { name: string | undefined; version: string | undefined }) {
  const runtimeVersion = /^runtime:(\d+)$/.exec(version ?? "")?.[1];

  if (!name || runtimeVersion === undefined) {
    return <Fact name="Strategy" value={name ? `${name} ${version ?? ""}`.trim() : undefined} />;
  }

  return (
    <div className="sk-side__fact">
      <dt>Strategy</dt>
      <dd>
        <Link to={`/strategies/${encodeURIComponent(name)}/versions/${runtimeVersion}`}>
          {name} {version}
        </Link>
      </dd>
    </div>
  );
}

function Fact({ name, value }: { name: string; value: string | undefined }) {
  if (!value) {
    return null;
  }

  return (
    <div className="sk-side__fact">
      <dt>{name}</dt>
      <dd>{value}</dd>
    </div>
  );
}
