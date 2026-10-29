import { useRef } from "react";
import { useParams } from "react-router";
import { isRunning, useInvestigation, useSteps, useStopWatcher, useWatchers } from "../../api/investigations";
import { mergeBySeq, useInvestigationStream } from "../../api/useInvestigationStream";
import { QueryError } from "../../components/QueryError";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import { EventsCard } from "./EventsCard";
import { FollowUpBox } from "./FollowUpBox";
import { PageHeader } from "./PageHeader";
import { ReportSections } from "./ReportSections";
import { SideColumn } from "./SideColumn";
import { StepTimeline } from "./StepTimeline";
import { VerdictPanel } from "./VerdictPanel";
import { WatcherBox } from "./WatcherBox";
import "./InvestigationPage.css";

/**
 * One investigation: the verdict first, then the watchers, the report, the
 * record of the run and, for responders, the follow-up box. While a run goes,
 * and while a follow-up question waits for its run, the page follows the
 * server's stream; it also reads the investigation again every 30 seconds
 * when nothing runs, so a run that someone started in Slack shows up.
 */
export default function InvestigationPage() {
  const { id = "" } = useParams();
  const me = useCurrentUser();
  const canAct = hasRole(me.role, "responder");
  const followUpField = useRef<HTMLTextAreaElement>(null);

  const investigation = useInvestigation(id);
  const stored = useSteps(id);
  const watchers = useWatchers(id);
  const stopWatcher = useStopWatcher(id);

  const inv = investigation.data;
  const storedSteps = stored.data?.pages.flatMap((page) => page.steps) ?? [];

  // The stream sends the steps after the last one the page has, while a run
  // goes and while a question waits for its run to start.
  const waiting = (inv?.pendingFollowUps.length ?? 0) > 0;
  const stream = useInvestigationStream({
    id,
    enabled: inv !== undefined && stored.isSuccess && (isRunning(inv) || waiting),
    after: storedSteps.at(-1)?.seq,
  });
  const steps = mergeBySeq(storedSteps, stream.steps);

  if (investigation.isPending) {
    return (
      <p role="status" className="sk-loading">
        Loading
      </p>
    );
  }

  if (investigation.isError) {
    return (
      <div className="sk-page">
        <QueryError error={investigation.error} onRetry={() => void investigation.refetch()} />
      </div>
    );
  }

  return (
    <div className="sk-inv">
      <div className="sk-inv__main">
        <PageHeader inv={investigation.data} canAct={canAct} onAskFollowUp={() => followUpField.current?.focus()} />
        <VerdictPanel inv={investigation.data} steps={steps.length} />
        <WatcherBox
          watchers={watchers.data ?? []}
          canStop={canAct}
          onStop={(watcher) => stopWatcher.mutate(watcher.id)}
          stopping={stopWatcher.isPending ? stopWatcher.variables : undefined}
        />
        {stopWatcher.isError && (
          <p role="alert" className="sk-field__error">
            {stopWatcher.error.message}
          </p>
        )}
        {investigation.data.report && <ReportSections report={investigation.data.report} />}
        {investigation.data.pendingFollowUps.length > 0 && (
          <section aria-labelledby="pending-heading">
            <h2 id="pending-heading" className="sk-inv__heading">
              Waiting for an answer
            </h2>
            <ul className="sk-inv__pending">
              {investigation.data.pendingFollowUps.map((question, i) => (
                <li key={i} className="sk-plain">
                  {question.author ? `${question.author}: ` : ""}
                  {question.text}
                </li>
              ))}
            </ul>
          </section>
        )}
        <EventsCard events={investigation.data.events} total={investigation.data.eventCount} />
        <StepTimeline
          steps={steps}
          live={isRunning(investigation.data) || waiting}
          hasMore={stored.hasNextPage}
          loadingMore={stored.isFetchingNextPage}
          onLoadMore={() => void stored.fetchNextPage()}
        />
        {canAct && <FollowUpBox investigationId={id} textareaRef={followUpField} />}
      </div>
      <SideColumn inv={investigation.data} />
    </div>
  );
}
