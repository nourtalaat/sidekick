import type { ReactNode } from "react";
import { Link } from "react-router";
import type { RuntimeRule } from "../../api/config";
import { Card } from "../../components/Card";
import { formatTime } from "../rules/format";
import { ConfigValue } from "./ConfigValue";
import "./SettingsPage.css";

interface RuntimeRulesCardProps {
  /** The name of the card, such as "Listeners". */
  title: string;
  rules: readonly RuntimeRule[];
  /** The page where the rules are changed. */
  to: string;
  linkLabel: string;
  /** What the card says when it has no rules. */
  empty: string;
}

/** The listeners or the routes that were added at runtime, with a link to the page that changes them. */
export function RuntimeRulesCard({ title, rules, to, linkLabel, empty }: RuntimeRulesCardProps) {
  return (
    <Card title={title} level={3}>
      <div className="sk-config__rules">
        {rules.length === 0 ? (
          <p className="sk-config__muted">{empty}</p>
        ) : (
          <ul className="sk-config__rule-list">
            {rules.map((rule) => (
              <li key={rule.name}>
                <RuleItem rule={rule} />
              </li>
            ))}
          </ul>
        )}
        <p>
          <Link to={to}>{linkLabel}</Link>
        </p>
      </div>
    </Card>
  );
}

function RuleItem({ rule }: { rule: RuntimeRule }) {
  return (
    <>
      <p className="sk-config__rule-name">{rule.name}</p>
      <dl className="sk-config__map">
        <Fact term="State">{rule.enabled ? "On" : "Off"}</Fact>
        <Fact term="Version">{rule.version}</Fact>
        <Fact term="Changed by">{rule.updatedBy}</Fact>
        <Fact term="Changed at">{formatTime(rule.updatedAt)}</Fact>
        <Fact term="Settings">
          <ConfigValue value={rule.config} label={rule.name} />
        </Fact>
      </dl>
    </>
  );
}

function Fact({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className="sk-config__entry">
      <dt>{term}</dt>
      <dd>{children}</dd>
    </div>
  );
}
