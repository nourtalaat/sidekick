import { useState } from "react";
import { CODE_STALE, CODE_VALIDATION, SaveError } from "../../api/rules";
import {
  CODE_NOT_PASSED,
  useApproveProposal,
  useEditProposal,
  useRejectProposal,
  type StrategyProposal,
  type StrategyProposalApproval,
} from "../../api/strategyProposals";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { CopyButton } from "../../components/CopyButton";
import { TextArea } from "../../components/TextArea";
import { TextField } from "../../components/TextField";
import { saveProblems } from "../strategies/problems";

const EDITOR_ROWS = 20;
const CODE_UNCHANGED = "unchanged";

/** The address of a download of text, which needs no server and no object URL. */
function downloadUrl(text: string): string {
  return `data:text/yaml;charset=utf-8,${encodeURIComponent(text)}`;
}

/** Says in words why an approval, an edit or a rejection was refused. */
function Refused({ error }: { error: SaveError }) {
  if (error.code === CODE_NOT_PASSED) {
    return (
      <Alert tone="danger" title="This proposal cannot be applied">
        <p>It has not passed its benchmark gate, or someone decided on it already. There is no way around the gate.</p>
      </Alert>
    );
  }

  if (error.code === CODE_STALE) {
    return (
      <Alert tone="warning" title="The strategy changed since this was drafted">
        <p>Reject the proposal, or edit it and save it to run the gate again on the strategy as it is now.</p>
      </Alert>
    );
  }

  if (error.code === CODE_UNCHANGED) {
    return (
      <Alert tone="warning" title="Nothing to apply">
        <p>The strategy already holds this text.</p>
      </Alert>
    );
  }

  if (error.code === CODE_VALIDATION) {
    const { body, reason } = saveProblems(error.fields);

    return (
      <Alert tone="danger" title="Not saved. The strategy has problems">
        <ul>
          {[...(reason ? [`reason: ${reason}`] : []), ...body].map((problem) => (
            <li key={problem}>{problem}</li>
          ))}
        </ul>
      </Alert>
    );
  }

  return (
    <Alert tone="danger" title="That did not work">
      <p>{error.message}</p>
    </Alert>
  );
}

/**
 * What an admin can do with a proposal. Approving is for a proposal that
 * passed its gate and nothing else; the button is off for any other, and the
 * server refuses it too. A strategy in a file cannot be changed by Sidekick,
 * so approving it gives the YAML to commit.
 */
export function ProposalActions({ proposal }: { proposal: StrategyProposal }) {
  const approve = useApproveProposal(proposal.id);
  const reject = useRejectProposal(proposal.id);
  const edit = useEditProposal(proposal.id);

  const [editing, setEditing] = useState(false);
  const [body, setBody] = useState(proposal.body ?? "");
  const [reason, setReason] = useState("");
  const [approval, setApproval] = useState<StrategyProposalApproval>();

  const error = [approve.error, reject.error, edit.error].find((e): e is SaveError => e instanceof SaveError);
  const forFile = proposal.baseOrigin === "file";
  const canEdit = proposal.status === "passed" || proposal.status === "failed";
  const canReject = ["pending", "benchmarking", "passed", "failed"].includes(proposal.status);
  const decided = proposal.status === "applied" || proposal.status === "approved" || proposal.status === "rejected";

  if (approval) {
    return (
      <Alert tone="success" title={approval.yaml ? "Approved. Commit the new strategy file" : `Applied as version ${approval.version}`}>
        {approval.yaml ? (
          <>
            <p>Sidekick cannot change a strategy file. Put this text in the file and deploy it.</p>
            <p>
              <a href={downloadUrl(approval.yaml)} download={`${proposal.name}.yaml`}>
                Download {proposal.name}.yaml
              </a>{" "}
              <CopyButton text={approval.yaml} label="Copy the YAML" />
            </p>
            <pre className="sk-proposal__yaml">{approval.yaml}</pre>
          </>
        ) : (
          <p>New investigations use it.</p>
        )}
      </Alert>
    );
  }

  if (decided) {
    return <p>This proposal is {proposal.status}. Nothing more can be done with it.</p>;
  }

  return (
    <section aria-label="Decide" className="sk-proposal__section">
      {error && <Refused error={error} />}
      <div className="sk-proposal__buttons">
        <Button
          variant="primary"
          disabled={!proposal.approvable || approve.isPending}
          aria-describedby={proposal.approvable ? undefined : `approve-note-${proposal.id}`}
          onClick={() => approve.mutate(undefined, { onSuccess: setApproval })}
        >
          {approve.isPending ? "Applying" : forFile ? "Approve and get the YAML" : "Approve and save as a new version"}
        </Button>
        {canEdit && <Button onClick={() => setEditing((on) => !on)}>{editing ? "Stop editing" : "Edit"}</Button>}
      </div>
      {!proposal.approvable && (
        <p id={`approve-note-${proposal.id}`} className="sk-proposal__note">
          Only a proposal that passed the benchmark gate can be approved.
        </p>
      )}
      {proposal.approvable && proposal.stale && (
        <p className="sk-proposal__note">The strategy changed since this was drafted, so approving it will be refused. Edit it to run the gate again.</p>
      )}
      {editing && (
        <form
          className="sk-proposal__edit"
          onSubmit={(event) => {
            event.preventDefault();
            edit.mutate(body, { onSuccess: () => setEditing(false) });
          }}
        >
          <TextArea
            label="Strategy (YAML)"
            help="The name must stay the same. Saving sends the proposal through the benchmark gate again."
            mono
            rows={EDITOR_ROWS}
            spellCheck={false}
            value={body}
            onChange={(event) => setBody(event.target.value)}
          />
          <Button type="submit" disabled={edit.isPending}>
            {edit.isPending ? "Saving" : "Save and run the gate again"}
          </Button>
        </form>
      )}
      {canReject && (
        <form
          className="sk-proposal__edit"
          onSubmit={(event) => {
            event.preventDefault();
            reject.mutate(reason);
          }}
        >
          <TextField label="Reason for rejecting (optional)" value={reason} onChange={(event) => setReason(event.target.value)} />
          <Button type="submit" variant="destructive" disabled={reject.isPending}>
            {reject.isPending ? "Rejecting" : "Reject"}
          </Button>
        </form>
      )}
    </section>
  );
}
