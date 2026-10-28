import { Check, Copy } from "lucide-react";
import { useEffect, useState } from "react";
import "./CopyButton.css";

const COPIED_MS = 2000;

/** Copies text to the clipboard. The button says "Copied" for a moment, in words a screen reader announces. */
export function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) {
      return undefined;
    }

    const timer = setTimeout(() => setCopied(false), COPIED_MS);

    return () => clearTimeout(timer);
  }, [copied]);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
    } catch {
      // The browser refused, such as on a page that is not secure; the text is still on the page to select.
    }
  };

  return (
    <>
      <button type="button" className="sk-copy" onClick={() => void copy()}>
        {copied ? <Check size={16} aria-hidden="true" /> : <Copy size={16} aria-hidden="true" />}
        <span>{copied ? "Copied" : label}</span>
      </button>
      <span role="status" className="sk-visually-hidden">
        {copied ? "Copied to the clipboard" : ""}
      </span>
    </>
  );
}
