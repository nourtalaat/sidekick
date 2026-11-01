import { useState } from "react";
import { Link } from "react-router";
import type { StrategyVersion } from "../../api/strategies";
import { Button, LinkButton } from "../../components/Button";
import { Select } from "../../components/Select";
import { Table } from "../../components/Table";
import { formatTime } from "../rules/format";

interface VersionHistoryProps {
  name: string;
  /** The saved versions loaded so far, newest first. */
  versions: readonly StrategyVersion[];
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}

/** The saved versions of a runtime strategy, each with a link to its text and to what it changed, and a way to compare any two. */
export function VersionHistory({ name, versions, hasMore, loadingMore, onLoadMore }: VersionHistoryProps) {
  const base = `/strategies/${encodeURIComponent(name)}`;
  const newest = versions[0]?.version;
  const [from, setFrom] = useState<string>();
  const [to, setTo] = useState<string>();

  const options = versions.map((v) => ({ value: String(v.version), label: `Version ${v.version}` }));
  const fromValue = from ?? String(versions[1]?.version ?? newest);
  const toValue = to ?? String(newest);

  return (
    <>
      <Table label={`Versions of ${name}`}>
        <thead>
          <tr>
            <th scope="col">Version</th>
            <th scope="col">Saved</th>
            <th scope="col">By</th>
            <th scope="col">Reason</th>
            <th scope="col">
              <span className="sk-visually-hidden">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {versions.map((v) => (
            <tr key={v.version}>
              <th scope="row">
                <Link to={`${base}/versions/${v.version}`} aria-label={`Version ${v.version}`}>
                  {v.version}
                </Link>
                {v.current && <span className="sk-table__secondary">Current</span>}
              </th>
              <td>
                <time dateTime={v.createdAt}>{formatTime(v.createdAt)}</time>
              </td>
              <td>{v.authorName ?? <span className="sk-table__secondary">Unknown</span>}</td>
              <td>{v.reason}</td>
              <td>
                {v.version > 1 && (
                  <Link to={`${base}/diff?from=${v.version - 1}&to=${v.version}`} aria-label={`Changes in version ${v.version}`}>
                    Changes
                  </Link>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      {hasMore && (
        <div>
          <Button disabled={loadingMore} onClick={onLoadMore}>
            {loadingMore ? "Loading" : "Load older versions"}
          </Button>
        </div>
      )}
      {versions.length > 1 && (
        <form className="sk-strategy-compare" onSubmit={(event) => event.preventDefault()}>
          <Select label="From version" options={options} value={fromValue} onChange={(event) => setFrom(event.target.value)} />
          <Select label="To version" options={options} value={toValue} onChange={(event) => setTo(event.target.value)} />
          <LinkButton to={`${base}/diff?from=${fromValue}&to=${toValue}`}>
            Show the changes
          </LinkButton>
        </form>
      )}
    </>
  );
}
