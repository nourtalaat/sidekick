import type { Proposal } from "../../api/proposals";
import { formatTime } from "../../format";
import { safeHttpUrl } from "../../safeUrl";
import { StatusBadge, type Status } from "../Badge";
import "./PullRequest.css";
import type { SectionProps } from "./section";

type PullRequestState = NonNullable<Proposal["pullRequest"]>;

const STATE: Record<PullRequestState["state"], { label: string; status: Status }> = {
  open: { label: "Open", status: "info" },
  merged: { label: "Merged", status: "success" },
  closed: { label: "Closed", status: "neutral" },
};

/**
 * What became of the pull request a proposal opened: a link to it, its state,
 * when it was merged or closed, and when Sidekick last read it from the host.
 * The host is read once a day, so the state can be a day old. Each line of
 * the list says one thing about the pull request; a line that says more goes
 * after the state line. The section shows nothing for a proposal with no
 * pull request.
 */
export function PullRequest({ proposal }: SectionProps) {
  const pullRequest = proposal.pullRequest;

  if (!pullRequest) {
    return null;
  }

  return (
    <section aria-label="Pull request" className="sk-proposal__pr">
      <p className="sk-proposal__label">Pull request</p>
      <ul className="sk-proposal__pr-lines">
        <li>
          <StateLine pullRequest={pullRequest} />
        </li>
      </ul>
    </section>
  );
}

/** The state line: the link, the state as an icon and a word, and the times. */
function StateLine({ pullRequest }: { pullRequest: PullRequestState }) {
  const { label, status } = STATE[pullRequest.state];
  const name = `${pullRequest.repo} #${pullRequest.number}`;
  const href = safeHttpUrl(pullRequest.url);

  return (
    <span className="sk-proposal__pr-state">
      {href ? <a href={href}>{name}</a> : <span className="sk-proposal__pr-name">{name}</span>}
      <StatusBadge status={status}>{label}</StatusBadge>
      {pullRequest.state === "merged" && <Moment label="Merged" at={pullRequest.mergedAt} />}
      {pullRequest.state === "closed" && <Moment label="Closed" at={pullRequest.closedAt} />}
      <Moment label="Checked" at={pullRequest.checkedAt} />
    </span>
  );
}

/** A label and a time, or nothing when there is no time to show. */
function Moment({ label, at }: { label: string; at: string | undefined }) {
  const text = formatTime(at);

  if (!text) {
    return null;
  }

  return (
    <span className="sk-proposal__pr-moment">
      {label} <time dateTime={at}>{text}</time>
    </span>
  );
}
