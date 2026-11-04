import { CircleHelp, CircleSlash, GitMerge, GitPullRequest, GitPullRequestClosed, Hourglass, type LucideIcon } from "lucide-react";
import type { Proposal } from "../../api/proposals";
import { formatTime } from "../../format";
import { safeHttpUrl } from "../../safeUrl";
import { StatusBadge, type Status } from "../Badge";
import "./PullRequest.css";
import type { SectionProps } from "./section";

type PullRequestState = NonNullable<Proposal["pullRequest"]>;

const STATE: Record<PullRequestState["state"], { label: string; status: Status; icon: LucideIcon }> = {
  open: { label: "Open", status: "info", icon: GitPullRequest },
  merged: { label: "Merged", status: "success", icon: GitMerge },
  closed: { label: "Closed", status: "neutral", icon: GitPullRequestClosed },
};

type CIState = NonNullable<PullRequestState["ci"]>;

const CI_STATE: Record<CIState["state"], { label: string; status: Status; icon?: LucideIcon; spinning?: boolean }> = {
  pending: { label: "Running", status: "info", spinning: true },
  passed: { label: "Passed", status: "success" },
  failed: { label: "Failed", status: "danger" },
  none: { label: "None", status: "neutral", icon: CircleSlash },
  unknown: { label: "Unknown", status: "warning", icon: CircleHelp },
  timed_out: { label: "Not finished", status: "warning", icon: Hourglass },
};

/**
 * What became of the pull request a proposal opened: a link to it, its state,
 * when it was merged or closed, and when Sidekick last read it from the host.
 * The host is read once a day, so the state can be a day old. Each line of
 * the list says one thing about the pull request; a line that says more goes
 * after the state line. The CI line follows the state line when Sidekick
 * follows the pull request's CI. The section shows nothing for a proposal
 * with no pull request.
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
        {pullRequest.ci && (
          <li>
            <CILine ci={pullRequest.ci} />
          </li>
        )}
      </ul>
    </section>
  );
}

/** The state line: the link, the state as an icon and a word, and the times. */
function StateLine({ pullRequest }: { pullRequest: PullRequestState }) {
  const { label, status, icon } = STATE[pullRequest.state];
  const name = `${pullRequest.repo} #${pullRequest.number}`;
  const href = safeHttpUrl(pullRequest.url);

  return (
    <span className="sk-proposal__pr-state">
      {href ? <a href={href}>{name}</a> : <span className="sk-proposal__pr-name">{name}</span>}
      <StatusBadge status={status} icon={icon}>
        {label}
      </StatusBadge>
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

/**
 * The CI line: its state as an icon and a word, what Sidekick found, the
 * failed checks by name, and when. The names come from the host, so they are
 * text, and a link only when the address is http or https.
 */
function CILine({ ci }: { ci: CIState }) {
  const { label, status, icon, spinning } = CI_STATE[ci.state];

  return (
    <div className="sk-proposal__ci">
      <span className="sk-proposal__pr-state">
        <span className="sk-proposal__ci-label">CI</span>
        <StatusBadge status={status} icon={icon} spinning={spinning}>
          {label}
        </StatusBadge>
        <span className="sk-proposal__ci-summary">{summary(ci)}</span>
        {ci.reportedAt ? <Moment label="Reported" at={ci.reportedAt} /> : <Moment label="Checked" at={ci.checkedAt} />}
      </span>
      {ci.failed && ci.failed.length > 0 && (
        <ul className="sk-proposal__ci-failed" aria-label="Failed checks">
          {ci.failed.map((check, i) => (
            <li key={i}>
              <CheckName name={check.name} url={check.url} />
            </li>
          ))}
          {ci.failedCount > ci.failed.length && <li>and {ci.failedCount - ci.failed.length} more</li>}
        </ul>
      )}
    </div>
  );
}

/** One failed check: its name, linked when it has an http or https address. */
function CheckName({ name, url }: { name: string; url: string | undefined }) {
  const href = safeHttpUrl(url);

  return href ? <a href={href}>{name}</a> : <span className="sk-proposal__pr-name">{name}</span>;
}

/** The sentence beside the CI badge, in Sidekick's own words. */
function summary(ci: CIState): string {
  switch (ci.state) {
    case "passed":
      return `${ci.passed} of ${checks(ci.total)} passed`;
    case "failed":
      return `${ci.failedCount} of ${checks(ci.total)} failed`;
    case "none":
      return "No check or pipeline started";
    case "unknown":
      return ci.message ?? "Sidekick could not read the CI";
    case "timed_out":
      return `Sidekick stopped waiting; ${ci.passed} of ${checks(ci.total)} had passed`;
    default:
      return ci.total > 0 ? `${ci.passed} of ${checks(ci.total)} passed so far` : "Waiting for the checks";
  }
}

/** A number of checks, such as "1 check" or "7 checks". */
function checks(n: number): string {
  return n === 1 ? "1 check" : `${n} checks`;
}
