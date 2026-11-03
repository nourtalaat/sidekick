import { TriangleAlert } from "lucide-react";
import { useState } from "react";
import { useFileDiff, type ProposalDiffLine, type ProposalFile, type ProposalFileDiff } from "../../api/proposals";
import { Button } from "../Button";
import type { SectionProps } from "./section";
import "./FileDiffs.css";

/** Up to this many files, each file starts open; with more, they start closed. */
const OPEN_UP_TO = 3;

const STATUS_WORD: Record<ProposalFile["status"], string> = { added: "Added", changed: "Changed", deleted: "Deleted" };
const SIGN: Record<ProposalDiffLine["kind"], string> = { context: "", added: "+", removed: "-" };
const SPOKEN: Record<ProposalDiffLine["kind"], string> = { context: "", added: "Added: ", removed: "Removed: " };

const FINAL_NEWLINE_NOTE: Record<NonNullable<ProposalFileDiff["finalNewline"]>, string> = {
  added: "The new file ends with a newline, which the old file did not.",
  removed: "The new file ends without a newline, which the old file had.",
};

/**
 * The files of a pull request proposal, one disclosure each with its path,
 * what happens to it and how many lines it adds and removes. A person opens a
 * file to read its line diff, which the server computes then. Nothing shows for
 * a proposal without files.
 */
export function FileDiffs({ proposal }: SectionProps) {
  const files = proposal.files ?? [];

  if (files.length === 0) {
    return null;
  }

  return (
    <section aria-label="Files" className="sk-files">
      <h4 className="sk-proposal__label">Files</h4>
      <ul className="sk-files__list">
        {files.map((file) => (
          <li key={file.path}>
            <FileDiff proposalId={proposal.id} file={file} startsOpen={files.length <= OPEN_UP_TO} />
          </li>
        ))}
      </ul>
    </section>
  );
}

function FileDiff({ proposalId, file, startsOpen }: { proposalId: string; file: ProposalFile; startsOpen: boolean }) {
  const [open, setOpen] = useState(startsOpen);

  return (
    <details className="sk-file" open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary className="sk-file__summary">
        <span className="sk-file__path">{file.path}</span>
        <span className="sk-file__status">{STATUS_WORD[file.status]}</span>
        <span aria-hidden="true" className="sk-file__counts">
          +{file.linesAdded} -{file.linesRemoved}
        </span>
        <span className="sk-visually-hidden">
          {file.linesAdded} {file.linesAdded === 1 ? "line" : "lines"} added, {file.linesRemoved} {file.linesRemoved === 1 ? "line" : "lines"} removed
        </span>
      </summary>
      {open && <FileBody proposalId={proposalId} file={file} />}
    </details>
  );
}

function FileBody({ proposalId, file }: { proposalId: string; file: ProposalFile }) {
  const [full, setFull] = useState(false);
  const query = useFileDiff(proposalId, file.path, full, file.diffAvailable);

  if (!file.diffAvailable) {
    return <p className="sk-proposal__note">Sidekick cannot compare this file here: the action that made the proposal cannot read it from its host.</p>;
  }

  if (query.isError) {
    return (
      <div className="sk-file__error">
        <p role="alert" className="sk-file__error-text">
          <TriangleAlert size={16} aria-hidden="true" />
          <span>Sidekick could not show this file. {query.error.message}</span>
        </p>
        <Button onClick={() => void query.refetch()}>Try again</Button>
      </div>
    );
  }

  if (!query.data) {
    return (
      <p role="status" className="sk-proposal__note">
        Reading the file from the host
      </p>
    );
  }

  const diff = query.data;

  return (
    <div className="sk-file__body">
      {diff.lines.length === 0 ? <p className="sk-proposal__note">The file has no changes.</p> : <DiffTable path={file.path} lines={diff.lines} />}
      {diff.finalNewline && <p className="sk-proposal__note">{FINAL_NEWLINE_NOTE[diff.finalNewline]}</p>}
      {diff.truncated && (
        <div className="sk-file__more">
          <p className="sk-proposal__note">The diff is cut after {diff.lines.length} lines.</p>
          <Button onClick={() => setFull(true)}>Show the whole file</Button>
        </div>
      )}
    </div>
  );
}

/** The lines as a table with their numbers, a sign and a spoken word as well as the tint, in a block that scrolls and takes focus. */
function DiffTable({ path, lines }: { path: string; lines: ProposalDiffLine[] }) {
  return (
    <div className="sk-file__diff" role="region" aria-label={`Changes in ${path}`} tabIndex={0}>
      <table className="sk-file__table">
        <thead className="sk-visually-hidden">
          <tr>
            <th scope="col">Line in the old file</th>
            <th scope="col">Line in the new file</th>
            <th scope="col">Text</th>
          </tr>
        </thead>
        <tbody>
          {lines.map((line, i) => (
            <DiffRows key={i} line={line} previous={lines[i - 1]} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** One line, preceded by a note when lines were left out between it and the line before. */
function DiffRows({ line, previous }: { line: ProposalDiffLine; previous: ProposalDiffLine | undefined }) {
  return (
    <>
      {skipsLines(line, previous) && (
        <tr className="sk-file__gap">
          <td colSpan={3}>Unchanged lines left out</td>
        </tr>
      )}
      <tr className={`sk-file__line sk-file__line--${line.kind}`}>
        <td className="sk-file__number">{line.oldLine}</td>
        <td className="sk-file__number">{line.newLine}</td>
        <td className="sk-file__text">
          <span aria-hidden="true" className="sk-file__sign">
            {SIGN[line.kind]}
          </span>
          <span className="sk-visually-hidden">{SPOKEN[line.kind]}</span>
          {line.text}
        </td>
      </tr>
    </>
  );
}

/** Whether lines of the file lie between the two lines. The first line follows nothing, so it skips lines when it is not line 1. */
function skipsLines(line: ProposalDiffLine, previous: ProposalDiffLine | undefined): boolean {
  if (previous === undefined) {
    return (line.oldLine ?? line.newLine ?? 1) > 1;
  }

  const oldJump = line.oldLine !== undefined && previous.oldLine !== undefined && line.oldLine > previous.oldLine + 1;
  const newJump = line.newLine !== undefined && previous.newLine !== undefined && line.newLine > previous.newLine + 1;

  return oldJump || newJump;
}
