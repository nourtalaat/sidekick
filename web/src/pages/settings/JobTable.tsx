import { Button } from "../../components/Button";
import { DeleteConfirm } from "../../components/DeleteConfirm";
import { Table } from "../../components/Table";
import type { Job } from "../../api/jobs";
import { formatTime } from "../rules/format";
import { LastError } from "./LastError";
import "./JobsTab.css";

/** Why a change to a job was refused, to show on its row. */
export interface Refusal {
  job: Job;
  action: "retry" | "discard";
  message: string;
}

interface JobTableProps {
  jobs: readonly Job[];
  /** The ID of the job whose Discard asks for confirmation, if any. */
  confirming: number | null;
  busy: boolean;
  refusals: readonly Refusal[];
  onRetry(job: Job): void;
  onDiscard(job: Job): void;
  onConfirm(job: Job): void;
  onCancel(job: Job): void;
}

const COLUMN_COUNT = 7;

/** The dead jobs as a table. Discard asks in the row below first. */
export function JobTable({ jobs, confirming, busy, refusals, onRetry, onDiscard, onConfirm, onCancel }: JobTableProps) {
  return (
    <Table label="Failed jobs">
      <thead>
        <tr>
          <th scope="col">Kind</th>
          <th scope="col">Route</th>
          <th scope="col">Attempts</th>
          <th scope="col">Created</th>
          <th scope="col">Died</th>
          <th scope="col">Last error</th>
          <th scope="col">Actions</th>
        </tr>
      </thead>
      <tbody>
        {jobs.map((job) => {
          const refusal = refusals.find((r) => r.job.id === job.id);

          return (
            <JobRows key={job.id} job={job} confirming={confirming === job.id} busy={busy} refusal={refusal} onRetry={onRetry} onDiscard={onDiscard} onConfirm={onConfirm} onCancel={onCancel} />
          );
        })}
      </tbody>
    </Table>
  );
}

/** The ID of the Discard button of a job, so focus can come back to it. */
export function discardButtonId(job: Job): string {
  return `sk-discard-${job.id}`;
}

interface JobRowsProps {
  job: Job;
  confirming: boolean;
  busy: boolean;
  refusal: Refusal | undefined;
  onRetry(job: Job): void;
  onDiscard(job: Job): void;
  onConfirm(job: Job): void;
  onCancel(job: Job): void;
}

function JobRows({ job, confirming, busy, refusal, onRetry, onDiscard, onConfirm, onCancel }: JobRowsProps) {
  return (
    <>
      <tr>
        <th scope="row">
          <code>{job.kind}</code>
        </th>
        <td>{job.route ?? "none"}</td>
        <td>{`${job.attempts} of ${job.maxAttempts}`}</td>
        <td>
          <Time iso={job.createdAt} />
        </td>
        <td>{job.finishedAt ? <Time iso={job.finishedAt} /> : null}</td>
        <td>
          <LastError jobId={job.id} text={job.lastError} />
        </td>
        <td>
          <div className="sk-table__actions">
            <Button disabled={busy} aria-label={`Retry ${job.kind} job ${job.id}`} onClick={() => onRetry(job)}>
              Retry
            </Button>
            <Button id={discardButtonId(job)} disabled={busy} aria-label={`Discard ${job.kind} job ${job.id}`} onClick={() => onDiscard(job)}>
              Discard
            </Button>
          </div>
          {refusal && (
            <p role="alert" className="sk-jobs__refused">
              <strong>{refusal.action === "retry" ? "Not retried." : "Not discarded."}</strong> {refusal.message}
            </p>
          )}
        </td>
      </tr>
      {confirming && (
        <tr>
          <td colSpan={COLUMN_COUNT}>
            <DeleteConfirm subject={`${job.kind} job`} verb="Discard" busy={busy} onConfirm={() => onConfirm(job)} onCancel={() => onCancel(job)}>
              <p>
                <strong>{job.kind}</strong> job {job.id}, route {job.route ?? "none"}, {job.attempts} of {job.maxAttempts} attempts.
              </p>
              <p>It will not run again. This cannot be undone.</p>
            </DeleteConfirm>
          </td>
        </tr>
      )}
    </>
  );
}

function Time({ iso }: { iso: string }) {
  return <time dateTime={iso}>{formatTime(iso)}</time>;
}
