import type { Evidence, Report } from "../../api/investigations";
import { StatusBadge } from "../../components/Badge";
import { safeHttpUrl } from "../../safeUrl";
import "./ReportSections.css";

/**
 * The body of the report: cause and impact, findings with their evidence,
 * fixes and the follow-up questions with their answers. Every text came from
 * the model or from a source, so it is plain text; a link opens only when it
 * is an http or https address.
 */
export function ReportSections({ report }: { report: Report }) {
  const falsePositive = report.falsePositive;

  return (
    <div className="sk-report">
      {report.cause && <TextSection title="Likely cause" text={report.cause} />}
      {falsePositive && <TextSection title="Why it fired" text={falsePositive.whyFired} />}
      {report.impact && <TextSection title="Impact" text={report.impact} />}
      {falsePositive && falsePositive.prevention.length > 0 && (
        <section>
          <h2 className="sk-report__title">How to stop it</h2>
          <ul className="sk-report__list">
            {falsePositive.prevention.map((p, i) => (
              <li key={i}>
                <strong>{p.kind}</strong>: {p.detail}
              </li>
            ))}
          </ul>
        </section>
      )}
      {report.findings.length > 0 && (
        <section>
          <h2 className="sk-report__title">Findings</h2>
          <ol className="sk-report__list">
            {report.findings.map((finding) => (
              <li key={finding.id}>
                <p className="sk-plain">{finding.claim}</p>
                {finding.evidence.length === 0 ? <StatusBadge status="neutral">Hypothesis</StatusBadge> : <EvidenceList evidence={finding.evidence} />}
              </li>
            ))}
          </ol>
        </section>
      )}
      {report.fixes && report.fixes.length > 0 && (
        <section>
          <h2 className="sk-report__title">Proposed fixes</h2>
          <ol className="sk-report__list">
            {report.fixes.map((fix) => (
              <li key={fix.id}>
                <strong className="sk-plain">{fix.title}</strong>
                <p className="sk-plain">{fix.detail}</p>
              </li>
            ))}
          </ol>
        </section>
      )}
      {report.followUps && report.followUps.length > 0 && (
        <section>
          <h2 className="sk-report__title">Follow-up questions</h2>
          <ul className="sk-report__list">
            {report.followUps.map((followUp) => (
              <li key={`${followUp.run}-${followUp.at}`}>
                {followUp.questions.map((q, i) => (
                  <p key={i} className="sk-plain">
                    <strong>{q.author ? `${q.author} asked` : "Asked"}:</strong> {q.text}
                  </p>
                ))}
                <p className="sk-plain">
                  <strong>{followUp.answered ? "Answer" : "No answer"}:</strong> {followUp.answer}
                </p>
                {followUp.evidence && followUp.evidence.length > 0 && <EvidenceList evidence={followUp.evidence} />}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

function TextSection({ title, text }: { title: string; text: string }) {
  return (
    <section>
      <h2 className="sk-report__title">{title}</h2>
      <p className="sk-plain">{text}</p>
    </section>
  );
}

function EvidenceList({ evidence }: { evidence: readonly Evidence[] }) {
  return (
    <ul className="sk-evidence">
      {evidence.map((e, i) => {
        const url = safeHttpUrl(e.url);

        return (
          <li key={i}>
            {url ? (
              <a href={url} target="_blank" rel="noopener noreferrer">
                {e.title}
              </a>
            ) : (
              e.title
            )}
            <span className="sk-evidence__kind"> ({e.kind})</span>
          </li>
        );
      })}
    </ul>
  );
}
