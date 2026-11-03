import { useEffect, useRef, useState, type ReactNode } from "react";
import { useRevokeToken, type Token } from "../../api/tokens";
import { Alert } from "../../components/Alert";
import { StatusBadge, type Status } from "../../components/Badge";
import { Button } from "../../components/Button";
import { DeleteConfirm } from "../../components/DeleteConfirm";
import { Table } from "../../components/Table";
import { saveErrorOf } from "../lessons/ProblemAlert";
import { formatTime } from "../rules/format";
import { ROLE_LABELS } from "./tokenForm";
import { tokenStatus, type TokenStatus } from "./tokenStatus";
import "./TokensTab.css";

interface TokenCellContext {
  now: Date;
  onRevoke(token: Token): void;
}

/** One column of the token table: its header and how a token fills its cell. */
export interface TokenColumn {
  id: string;
  header: string;
  /** Makes the cell of each row the header of that row, for a screen reader. */
  rowHeader?: boolean;
  cell(token: Token, context: TokenCellContext): ReactNode;
}

const STATUS_BADGES: Record<TokenStatus, { status: Status; label: string }> = {
  active: { status: "success", label: "Active" },
  expired: { status: "warning", label: "Expired" },
  revoked: { status: "danger", label: "Revoked" },
};

/** The columns of the token table, in the order they are shown. A new column is one more entry. */
export const TOKEN_COLUMNS: readonly TokenColumn[] = [
  { id: "name", header: "Name", rowHeader: true, cell: (token) => token.name },
  { id: "role", header: "Role", cell: (token) => <span className="sk-tokens__role">{ROLE_LABELS[token.role]}</span> },
  {
    id: "created",
    header: "Created",
    cell: (token) => (
      <>
        <Time iso={token.createdAt} />
        <span className="sk-table__secondary">
          by <code>{token.createdBy}</code>
        </span>
      </>
    ),
  },
  { id: "expires", header: "Expires", cell: (token) => <Time iso={token.expiresAt} /> },
  { id: "lastUsed", header: "Last used", cell: (token) => (token.lastUsedAt ? <Time iso={token.lastUsedAt} /> : "Never") },
  {
    id: "status",
    header: "Status",
    cell: (token, { now }) => {
      const badge = STATUS_BADGES[tokenStatus(token, now)];

      return (
        <>
          <StatusBadge status={badge.status}>{badge.label}</StatusBadge>
          {token.revokedAt && (
            <span className="sk-table__secondary">
              At <Time iso={token.revokedAt} />
            </span>
          )}
        </>
      );
    },
  },
  {
    id: "actions",
    header: "Actions",
    cell: (token, { now, onRevoke }) =>
      tokenStatus(token, now) === "active" ? (
        <Button id={revokeButtonId(token)} aria-label={`Revoke token ${token.name}`} onClick={() => onRevoke(token)}>
          Revoke
        </Button>
      ) : null,
  },
];

/**
 * The tokens as a table, in the order given. Expired and revoked rows are
 * dimmed and say so in words. Revoke asks in the row below first.
 */
export function TokenTable({ tokens, now }: { tokens: readonly Token[]; now: Date }) {
  const revoke = useRevokeToken();
  const [confirming, setConfirming] = useState<Token | null>(null);
  const [revoked, setRevoked] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  function begin(token: Token) {
    setRevoked(null);
    setFailure(null);
    setConfirming(token);
  }

  function cancel(token: Token) {
    setConfirming(null);
    document.getElementById(revokeButtonId(token))?.focus();
  }

  function confirm(token: Token) {
    revoke.mutate(token.id, {
      onSuccess: () => setRevoked(token.name),
      onError: (e) => setFailure(saveErrorOf(e).message),
      onSettled: () => setConfirming(null),
    });
  }

  return (
    <>
      {revoked !== null && <Notice tone="success" title="Token revoked" text={`${revoked} no longer works.`} />}
      {failure !== null && <Notice tone="danger" title="Could not revoke the token" text={failure} />}
      <Table label="API tokens">
        <thead>
          <tr>
            {TOKEN_COLUMNS.map((column) => (
              <th key={column.id} scope="col">
                {column.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {tokens.map((token) => (
            <TokenRows key={token.id} token={token} now={now} confirming={confirming?.id === token.id} busy={revoke.isPending} onRevoke={begin} onConfirm={confirm} onCancel={cancel} />
          ))}
        </tbody>
      </Table>
    </>
  );
}

interface TokenRowsProps {
  token: Token;
  now: Date;
  confirming: boolean;
  busy: boolean;
  onRevoke(token: Token): void;
  onConfirm(token: Token): void;
  onCancel(token: Token): void;
}

function TokenRows({ token, now, confirming, busy, onRevoke, onConfirm, onCancel }: TokenRowsProps) {
  const active = tokenStatus(token, now) === "active";

  return (
    <>
      <tr className={active ? undefined : "sk-tokens__row--inactive"}>
        {TOKEN_COLUMNS.map((column) => {
          const content = column.cell(token, { now, onRevoke });

          return column.rowHeader ? (
            <th key={column.id} scope="row">
              {content}
            </th>
          ) : (
            <td key={column.id}>{content}</td>
          );
        })}
      </tr>
      {confirming && (
        <tr>
          <td colSpan={TOKEN_COLUMNS.length}>
            <DeleteConfirm subject="token" verb="Revoke" busy={busy} onConfirm={() => onConfirm(token)} onCancel={() => onCancel(token)}>
              <p>
                <strong>{token.name}</strong>
              </p>
              <p>Anything that uses it stops working at once.</p>
            </DeleteConfirm>
          </td>
        </tr>
      )}
    </>
  );
}

/** A result of a revoke. It takes focus when it appears, so a screen reader reads it. */
function Notice({ tone, title, text }: { tone: "success" | "danger"; title: string; text: string }) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    ref.current?.focus();
  }, []);

  return (
    <Alert ref={ref} tone={tone} title={title}>
      <p>{text}</p>
    </Alert>
  );
}

function Time({ iso }: { iso: string }) {
  return <time dateTime={iso}>{formatTime(iso)}</time>;
}

function revokeButtonId(token: Token): string {
  return `sk-revoke-${token.id}`;
}
