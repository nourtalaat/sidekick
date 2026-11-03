import { safeHttpUrl } from "../../safeUrl";
import type { SectionProps } from "./section";

/** What became of a proposal that ran: the summary and links, or why it failed. */
export function Outcome({ proposal }: SectionProps) {
  if (proposal.result) {
    return (
      <div>
        <p className="sk-proposal__label">Result</p>
        <p className="sk-proposal__text">{proposal.result.summary}</p>
        <ul>
          {(proposal.result.links ?? []).map((link) => {
            const href = safeHttpUrl(link.url);

            return <li key={link.url}>{href ? <a href={href}>{link.title}</a> : link.title}</li>;
          })}
        </ul>
      </div>
    );
  }

  if (proposal.error) {
    return (
      <div>
        <p className="sk-proposal__label">{proposal.status === "failed" ? "Why it failed" : "The last attempt failed"}</p>
        <p className="sk-proposal__text">{proposal.error}</p>
      </div>
    );
  }

  return null;
}
