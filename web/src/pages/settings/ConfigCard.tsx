import { isRecord, isSecretRef } from "./configFilter";
import { ConfigValue } from "./ConfigValue";
import "./SettingsPage.css";

/** How many keys of a map the one-line summary of its card names. */
const SUMMARY_KEYS = 4;

/** The longest text a one-line summary shows, in characters. */
const SUMMARY_TEXT_CHARS = 60;

interface ConfigCardProps {
  /** The top-level key of the file config. */
  name: string;
  /** What the card shows when it opens: the whole value, or the part of it that the filter matched. */
  value: unknown;
  /** What the one-line summary describes: the whole value. */
  whole: unknown;
  /** Whether the card starts open. A card is a native disclosure, so the reader can change it afterwards. */
  open: boolean;
}

/** One top-level key of the file config as a card that opens, with a one-line summary while it is closed. */
export function ConfigCard({ name, value, whole, open }: ConfigCardProps) {
  return (
    <details className="sk-card sk-config__card" open={open}>
      <summary className="sk-config__summary">
        <span className="sk-config__card-title">{name}</span> <span className="sk-config__summary-text">{summaryOf(whole)}</span>
      </summary>
      <div className="sk-config__card-body">
        <ConfigValue value={value} label={name} />
      </div>
    </details>
  );
}

/** One line that says what is inside a value, such as "3 settings: provider, timeout, apiKeySecret". */
function summaryOf(value: unknown): string {
  if (isSecretRef(value)) {
    return "Secret reference";
  }

  if (Array.isArray(value)) {
    return value.length === 0 ? "Empty list" : `${value.length} ${value.length === 1 ? "item" : "items"}`;
  }

  if (isRecord(value)) {
    const keys = Object.keys(value);
    if (keys.length === 0) {
      return "Empty";
    }

    const shown = keys.slice(0, SUMMARY_KEYS).join(", ");

    return `${keys.length} ${keys.length === 1 ? "setting" : "settings"}: ${shown}${keys.length > SUMMARY_KEYS ? ", ..." : ""}`;
  }

  if (value === null || value === undefined) {
    return "Not set";
  }

  const text = String(value).split("\n")[0] ?? "";

  return text.length > SUMMARY_TEXT_CHARS ? `${text.slice(0, SUMMARY_TEXT_CHARS)}...` : text;
}
