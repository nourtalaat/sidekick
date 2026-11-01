import "./Diff.css";

/** The YAML of a strategy that cannot be changed, in a region the keyboard can focus and scroll. */
export function YamlText({ text }: { text: string }) {
  return (
    <div className="sk-yaml" role="region" aria-label="Strategy YAML" tabIndex={0}>
      <pre className="sk-yaml__text">{text}</pre>
    </div>
  );
}
