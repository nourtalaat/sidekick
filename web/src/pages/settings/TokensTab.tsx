import { useRef, useState } from "react";
import { useTokens } from "../../api/tokens";
import { Card } from "../../components/Card";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { NewTokenAlert } from "./NewTokenAlert";
import { NewTokenForm } from "./NewTokenForm";
import { TokenTable } from "./TokenTable";
import { sortTokens } from "./tokenStatus";
import "./TokensTab.css";

/** A token that was just made, with the secret that is shown once. */
interface CreatedToken {
  name: string;
  secret: string;
}

/**
 * The API tokens of Sidekick: a form that makes one, and the list of all of
 * them with a Revoke button on each one that still works. The secret of a new
 * token lives only in this component's state, so it is gone when the person
 * presses Done, makes another token or leaves the tab.
 */
export function TokensTab() {
  const tokens = useTokens();
  const [created, setCreated] = useState<CreatedToken | null>(null);
  const formRef = useRef<HTMLDivElement>(null);

  function done() {
    setCreated(null);
    formRef.current?.focus();
  }

  const now = new Date();

  return (
    <>
      <p className="sk-tokens__note">An API token lets a script or a CI job call the API without signing in. Only a signed-in admin can create or revoke tokens.</p>
      {created && <NewTokenAlert name={created.name} secret={created.secret} onDone={done} />}
      <div ref={formRef} tabIndex={-1} className="sk-tokens__create">
        <Card title="Create an API token">
          <NewTokenForm onStart={() => setCreated(null)} onCreated={(name, secret) => setCreated({ name, secret })} />
        </Card>
      </div>
      <section className="sk-tokens__list" aria-labelledby="sk-tokens-heading">
        <h2 id="sk-tokens-heading" className="sk-tokens__heading">
          Tokens
        </h2>
        {tokens.isPending && (
          <p role="status" className="sk-loading">
            Loading
          </p>
        )}
        {tokens.isError && <QueryError error={tokens.error} onRetry={() => void tokens.refetch()} />}
        {tokens.isSuccess && tokens.data.length === 0 && <EmptyState title="No API tokens yet">Use the form above to create one.</EmptyState>}
        {tokens.isSuccess && tokens.data.length > 0 && <TokenTable tokens={sortTokens(tokens.data, now)} now={now} />}
      </section>
    </>
  );
}
