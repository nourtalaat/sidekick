import { isRecord, isSecretRef, type SecretRef } from "./configFilter";
import "./ConfigValue.css";

/** A string longer than this many characters, or with a line break, scrolls in a block of its own. */
const LONG_TEXT_CHARS = 80;

interface ConfigValueProps {
  /** Any value of the config: text, number, true or false, null, a list or a map. */
  value: unknown;
  /** The name of the field the value belongs to, which names a scrolling block for a screen reader. */
  label: string;
}

/**
 * Shows any value of the config as plain text. A map is a definition list, a
 * list is a bullet list, and a long text scrolls in a block that takes focus.
 * An object with exactly the keys name and key is a reference to a Secret and
 * shows as its name and key; nothing here asks for a secret value.
 */
export function ConfigValue({ value, label }: ConfigValueProps) {
  if (isSecretRef(value)) {
    return <SecretReference reference={value} />;
  }

  if (Array.isArray(value)) {
    return <ListValue items={value} label={label} />;
  }

  if (isRecord(value)) {
    return <MapValue entries={Object.entries(value)} />;
  }

  if (typeof value === "string") {
    return <TextValue text={value} label={label} />;
  }

  if (value === null || value === undefined) {
    return <span className="sk-config__muted">Not set</span>;
  }

  return <>{String(value)}</>;
}

function SecretReference({ reference }: { reference: SecretRef }) {
  if (reference.name === "" && reference.key === "") {
    return <span className="sk-config__muted">No secret set</span>;
  }

  return (
    <span>
      Secret <code className="sk-config__code">{reference.name}</code>, key <code className="sk-config__code">{reference.key}</code>
    </span>
  );
}

function ListValue({ items, label }: { items: unknown[]; label: string }) {
  if (items.length === 0) {
    return <span className="sk-config__muted">Empty list</span>;
  }

  return (
    <ul className="sk-config__list">
      {items.map((item, index) => (
        <li key={index}>
          <ConfigValue value={item} label={`${label} ${index + 1}`} />
        </li>
      ))}
    </ul>
  );
}

function MapValue({ entries }: { entries: [string, unknown][] }) {
  if (entries.length === 0) {
    return <span className="sk-config__muted">Empty</span>;
  }

  return (
    <dl className="sk-config__map">
      {entries.map(([key, item]) => (
        <div key={key} className="sk-config__entry">
          <dt>{key}</dt>
          <dd>
            <ConfigValue value={item} label={key} />
          </dd>
        </div>
      ))}
    </dl>
  );
}

function TextValue({ text, label }: { text: string; label: string }) {
  if (text === "") {
    return <span className="sk-config__muted">Empty text</span>;
  }

  if (text.length > LONG_TEXT_CHARS || text.includes("\n")) {
    return (
      <div role="group" aria-label={`Value of ${label}`} tabIndex={0} className="sk-config__block">
        <pre>{text}</pre>
      </div>
    );
  }

  return <span className="sk-config__text">{text}</span>;
}
