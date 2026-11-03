import { Ban, Trash2 } from "lucide-react";
import { Link } from "react-router";
import type { UsedMemory as UsedMemoryExample } from "../../api/investigations";
import { VerdictBadge } from "../../components/Badge";
import { Card } from "../../components/Card";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import "./UsedMemory.css";

const SHORT_ID_CHARS = 8;

/** Why an example was chosen, in the words the prompt uses. */
const MATCH_WORDS: Record<UsedMemoryExample["match"], string> = {
  fingerprint: "the same alert fingerprint",
  service_alert: "the same service and alert name",
  text: "similar words",
  similar: "a similar description",
};

/**
 * The past investigations that the prompt of the run showed as examples, with
 * their text and state as they are now. The text came from alerts and the
 * model, so it is plain text. The card is not shown when nothing was used.
 */
export function UsedMemory({ examples }: { examples: readonly UsedMemoryExample[] | undefined }) {
  const me = useCurrentUser();

  if (!examples || examples.length === 0) {
    return null;
  }

  return (
    <Card title="Memory used">
      <p className="sk-memory__note">These are the entries as they are now; the prompt had their text at the time.</p>
      <ul className="sk-memory">
        {examples.map((example) => (
          <li key={example.investigationId} className="sk-memory__example">
            {example.verdict && <VerdictBadge verdict={example.verdict} />}
            <Link className="sk-memory__link" to={`/investigations/${example.investigationId}`}>
              {labelOf(example)}
            </Link>
            <p className="sk-memory__why">Chosen for {MATCH_WORDS[example.match]}</p>
            <State example={example} />
          </li>
        ))}
      </ul>
      {hasRole(me.role, "admin") && <Link to="/lessons?tab=memory">Manage memory</Link>}
    </Card>
  );
}

/** What an example became since the prompt: gone from memory, or disabled. */
function State({ example }: { example: UsedMemoryExample }) {
  if (!example.present) {
    return (
      <p className="sk-memory__state">
        <Trash2 size={16} aria-hidden="true" />
        No longer in memory
      </p>
    );
  }

  if (example.disabled) {
    return (
      <p className="sk-memory__state">
        <Ban size={16} aria-hidden="true" />
        Disabled since this run
      </p>
    );
  }

  return null;
}

function labelOf(example: UsedMemoryExample): string {
  return example.title || example.summary || `Investigation ${example.investigationId.slice(0, SHORT_ID_CHARS)}`;
}
