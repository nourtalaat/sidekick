import { useEffect, useRef, useState } from "react";
import { useDeadJobs, useDiscardJob, useRetryJob, type Job } from "../../api/jobs";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { JobTable, discardButtonId, type Refusal } from "./JobTable";
import "./JobsTab.css";

/** The result of a change, which takes focus when it appears so a screen reader reads it. */
interface Notice {
  id: number;
  title: string;
  text: string;
}

/**
 * The background jobs that failed every attempt, newest first, with Retry
 * and Discard on each. Rows come a page at a time. A row leaves the list
 * when its job is retried or discarded; the pages already read stay. When
 * the server refuses a change because the list is out of date, the reason
 * shows on the row and the list is read again.
 */
export function JobsTab() {
  const jobs = useDeadJobs();
  const retry = useRetryJob();
  const discard = useDiscardJob();
  const [confirming, setConfirming] = useState<number | null>(null);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [refusals, setRefusals] = useState<Refusal[]>([]);
  const noticeCount = useRef(0);

  const rows = jobs.data?.pages.flatMap((page) => page.jobs) ?? [];
  const busy = retry.isPending || discard.isPending;

  function begin() {
    setNotice(null);
    setRefusals([]);
  }

  function say(title: string, text: string) {
    noticeCount.current += 1;
    setNotice({ id: noticeCount.current, title, text });
  }

  function onRetry(job: Job) {
    begin();
    retry.mutate(job.id, {
      onSuccess: () => say("Job retried", `${describe(job)} is pending again.`),
      onError: (e) => setRefusals([{ job, action: "retry", message: e.message }]),
    });
  }

  function onDiscard(job: Job) {
    begin();
    discard.mutate(job.id, {
      onSuccess: () => say("Job discarded", `${describe(job)} is deleted and will not run again.`),
      onError: (e) => setRefusals([{ job, action: "discard", message: e.message }]),
      onSettled: () => setConfirming(null),
    });
  }

  function cancel(job: Job) {
    setConfirming(null);
    document.getElementById(discardButtonId(job))?.focus();
  }

  return (
    <>
      <p className="sk-jobs__note">
        A background job that fails every attempt is marked dead. Retry puts it back in the queue. Discard deletes it, so it never runs again. The input of a job is not shown. Sidekick also deletes old dead jobs by
        itself.
      </p>
      {notice && <NoticeAlert key={notice.id} notice={notice} />}
      {orphansOf(refusals, rows).map((refusal) => (
        <Alert key={refusal.job.id} tone="warning" title={`${describe(refusal.job)} was not ${refusal.action === "retry" ? "retried" : "discarded"}`}>
          <p>{refusal.message}</p>
        </Alert>
      ))}
      <section className="sk-jobs__list">
        <h2 className="sk-jobs__heading">
          Failed jobs
        </h2>
        {jobs.isPending && (
          <p role="status" className="sk-loading">
            Loading
          </p>
        )}
        {jobs.isError && <QueryError error={jobs.error} onRetry={() => void jobs.refetch()} />}
        {jobs.isSuccess && rows.length === 0 && !jobs.hasNextPage && <EmptyState title="No failed jobs.">Jobs that fail every attempt appear here.</EmptyState>}
        {rows.length > 0 && (
          <JobTable
            jobs={rows}
            confirming={confirming}
            busy={busy}
            refusals={refusals}
            onRetry={onRetry}
            onDiscard={(job) => {
              begin();
              setConfirming(job.id);
            }}
            onConfirm={onDiscard}
            onCancel={cancel}
          />
        )}
        {jobs.isSuccess && rows.length === 0 && jobs.hasNextPage && <p className="sk-jobs__note">No failed jobs are left in the pages read so far. Older ones may follow.</p>}
        {jobs.hasNextPage && (
          <div>
            <Button onClick={() => void jobs.fetchNextPage()} disabled={jobs.isFetchingNextPage}>
              {jobs.isFetchingNextPage ? "Loading" : "Load more"}
            </Button>
          </div>
        )}
      </section>
    </>
  );
}

function NoticeAlert({ notice }: { notice: Notice }) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    ref.current?.focus();
  }, []);

  return (
    <Alert ref={ref} tone="success" title={notice.title}>
      <p>{notice.text}</p>
    </Alert>
  );
}

/** The refusals of jobs that are no longer in the list, so their reason is not lost with the row. */
function orphansOf(refusals: Refusal[], rows: Job[]): Refusal[] {
  return refusals.filter((refusal) => !rows.some((job) => job.id === refusal.job.id));
}

function describe(job: Job): string {
  return `The ${job.kind} job ${job.id}`;
}
