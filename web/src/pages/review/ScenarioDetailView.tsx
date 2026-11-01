import { useState } from "react";
import { Link } from "react-router";
import { useReviewScenario, useScenario, useUpdateScenario, scenarioFileUrl, type ScenarioDetail, type ScenarioPair } from "../../api/scenarios";
import { StatusBadge } from "../../components/Badge";
import { Alert } from "../../components/Alert";
import { Button, ButtonLink } from "../../components/Button";
import { Card } from "../../components/Card";
import { Checkbox } from "../../components/Checkbox";
import { QueryError } from "../../components/QueryError";
import { TextArea } from "../../components/TextArea";
import { formatTime } from "../rules/format";
import { Highlighted } from "./highlight";
import "./Scenarios.css";

/** One scenario: what the exporter did, each part next to its original, the file to edit, and the review. */
export function ScenarioDetailView({ id }: { id: string }) {
  const scenario = useScenario(id);

  return (
    <section aria-label="Scenario" className="sk-scenarios">
      <p>
        <Link to="/review?tab=scenarios">Back to the scenarios</Link>
      </p>
      {scenario.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {scenario.isError && <QueryError error={scenario.error} onRetry={() => void scenario.refetch()} />}
      {scenario.isSuccess && <Detail key={scenario.data.scenario.revision} detail={scenario.data} />}
    </section>
  );
}

function Detail({ detail }: { detail: ScenarioDetail }) {
  const { scenario } = detail;
  const [body, setBody] = useState(detail.body);
  const edited = body !== detail.body;

  return (
    <>
      <header className="sk-scenario__header">
        <h2 className="sk-scenarios__title">{scenario.title}</h2>
        <StatusBadge status={scenario.status === "reviewed" ? "success" : "neutral"}>{scenario.status === "reviewed" ? "Reviewed" : "Draft"}</StatusBadge>
        {scenario.status === "reviewed" && <ButtonLink href={scenarioFileUrl(scenario.id)} download>Download</ButtonLink>}
      </header>
      <p className="sk-scenarios__id">
        <code>{scenario.id}</code>
        {scenario.investigationId && (
          <>
            {" from "}
            <Link to={`/investigations/${scenario.investigationId}`}>the investigation</Link>
          </>
        )}
      </p>
      <Notes detail={detail} />
      <Pairs detail={detail} />
      <Editor id={scenario.id} revision={scenario.revision} body={body} onChange={setBody} />
      <Review detail={detail} edited={edited} />
    </>
  );
}

function Notes({ detail }: { detail: ScenarioDetail }) {
  return (
    <Card title="What to check first" level={3}>
      <p>
        The exporter replaced emails, user names, account IDs, host names and IP addresses with placeholders such as <code>host-1</code>. It cannot find every sensitive value, for example a
        person's name in free text. Read each part against its original.
      </p>
      {detail.candidates.length > 0 ? (
        <>
          <p>It kept these because it could not tell what they are. They are marked in the cleaned text below.</p>
          <ul>
            {detail.candidates.map((c) => (
              <li key={`${c.path}:${c.start}`}>
                <code>{c.path}</code>: {c.reason}
              </li>
            ))}
          </ul>
        </>
      ) : (
        <p>It kept nothing that looks like an ID.</p>
      )}
      {detail.notes.length > 0 && (
        <>
          <p>What the exporter did:</p>
          <ul>
            {detail.notes.map((n, i) => (
              <li key={i}>{n.text}</li>
            ))}
          </ul>
        </>
      )}
    </Card>
  );
}

function Pairs({ detail }: { detail: ScenarioDetail }) {
  return (
    <section aria-labelledby="pairs-heading" className="sk-scenario__pairs">
      <h3 id="pairs-heading" className="sk-scenarios__subtitle">
        Original and cleaned
      </h3>
      {detail.pairs.map((pair) => (
        <PairView key={pair.ref} pair={pair} candidates={detail.candidates.filter((c) => c.path === pair.ref)} />
      ))}
    </section>
  );
}

function PairView({ pair, candidates }: { pair: ScenarioPair; candidates: ScenarioDetail["candidates"] }) {
  return (
    <Card title={pair.label} level={3}>
      <div className="sk-pair">
        <div className="sk-pair__side">
          <h4 className="sk-pair__caption">Original, as stored</h4>
          {pair.original !== undefined ? <pre className="sk-pair__text">{pair.original}</pre> : <p className="sk-pair__none">{pair.note ?? "Not shown."}</p>}
        </div>
        <div className="sk-pair__side">
          <h4 className="sk-pair__caption">Cleaned, as the file holds it</h4>
          <pre className="sk-pair__text">
            <Highlighted text={pair.cleaned} candidates={candidates} />
          </pre>
        </div>
      </div>
    </Card>
  );
}

function Editor({ id, revision, body, onChange }: { id: string; revision: number; body: string; onChange(next: string): void }) {
  const update = useUpdateScenario(id);
  const [open, setOpen] = useState(false);

  return (
    <section aria-labelledby="editor-heading" className="sk-scenario__editor">
      <h3 id="editor-heading" className="sk-scenarios__subtitle">
        Scenario file
      </h3>
      <div>
        <Button onClick={() => setOpen(!open)} aria-expanded={open}>
          Edit the scenario file
        </Button>
      </div>
      {open && (
        <>
          <p>Saving checks the file and puts the scenario back to draft, so it has to be reviewed again. A file with an email, a host name or an IP address is refused.</p>
          <TextArea label="Scenario file (YAML)" mono rows={24} value={body} onChange={(e) => onChange(e.target.value)} spellCheck={false} />
          {update.isError && (
            <Alert tone="danger" title="Could not save the file">
              <p className="sk-scenarios__problem">{update.error.message}</p>
            </Alert>
          )}
          <div>
            <Button onClick={() => update.mutate({ revision, body })} disabled={update.isPending}>
              {update.isPending ? "Saving" : "Save the file"}
            </Button>
          </div>
        </>
      )}
    </section>
  );
}

function Review({ detail, edited }: { detail: ScenarioDetail; edited: boolean }) {
  const { scenario } = detail;
  const review = useReviewScenario(scenario.id);
  const [checked, setChecked] = useState(false);

  if (scenario.status === "reviewed") {
    return (
      <Card title="Review" level={3}>
        <p role="status">
          Reviewed{scenario.reviewedBy ? ` by ${scenario.reviewedBy}` : ""}
          {scenario.reviewedAt ? ` on ${formatTime(scenario.reviewedAt)}` : ""}. It can be run and downloaded. Editing it makes it a draft again.
        </p>
      </Card>
    );
  }

  return (
    <Card title="Review" level={3}>
      <Checkbox
        label="I checked this for sensitive data"
        help="You read each part above against its original, and the file holds no email, host name, IP address, name or secret that should not leave this deployment."
        checked={checked}
        onChange={(e) => setChecked(e.target.checked)}
      />
      {edited && <p className="sk-scenarios__id">Save your edits before you review.</p>}
      {review.isError && (
        <Alert tone="danger" title="Could not mark it as reviewed">
          <p className="sk-scenarios__problem">{review.error.message}</p>
        </Alert>
      )}
      <div>
        <Button
          variant="primary"
          disabled={!checked || edited || review.isPending}
          onClick={() => review.mutate({ revision: scenario.revision, sensitiveDataChecked: true })}
        >
          {review.isPending ? "Saving" : "Mark as reviewed"}
        </Button>
      </div>
    </Card>
  );
}
