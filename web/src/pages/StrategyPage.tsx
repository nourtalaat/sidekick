import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { ApiError } from "../api/client";
import { strategyKeys, useStrategy, useStrategyVersion, useStrategyVersions, type StrategyDetail, type StrategyVersion } from "../api/strategies";
import { Alert } from "../components/Alert";
import { Button } from "../components/Button";
import { EmptyState } from "../components/EmptyState";
import { QueryError } from "../components/QueryError";
import { hasRole } from "../roles";
import { useCurrentUser } from "../session";
import { StrategyLayerBadge, StrategyStatusBadge } from "./strategies/badges";
import { StrategyEditor } from "./strategies/StrategyEditor";
import { YamlText } from "./strategies/YamlText";
import { VersionHistory } from "./strategies/VersionHistory";

const NOT_FOUND = 404;

/**
 * One strategy: its text, an editor for a runtime strategy, and the history of
 * its versions. A built-in strategy and a strategy file are read-only; a
 * built-in one can be copied into a runtime strategy of the same name.
 */
export function StrategyPage() {
  const { name = "" } = useParams();
  const strategy = useStrategy(name);

  if (strategy.isPending) {
    return (
      <p role="status" className="sk-loading">
        Loading
      </p>
    );
  }

  if (strategy.isError) {
    if (strategy.error instanceof ApiError && strategy.error.status === NOT_FOUND) {
      return (
        <div className="sk-page">
          <h1 className="sk-page__title">Strategy {name}</h1>
          <EmptyState title="No strategy has this name">
            <Link to="/strategies">Back to the strategies</Link>
          </EmptyState>
        </div>
      );
    }

    return <QueryError error={strategy.error} onRetry={() => void strategy.refetch()} />;
  }

  return <Loaded name={name} strategy={strategy.data} />;
}

function Loaded({ name, strategy }: { name: string; strategy: StrategyDetail }) {
  const me = useCurrentUser();
  const queryClient = useQueryClient();
  const [search] = useSearchParams();
  const canEdit = hasRole(me.role, "admin");

  const versions = useStrategyVersions(name);
  const list: StrategyVersion[] = versions.data?.pages.flatMap((p) => p.versions) ?? [];
  const current = list.find((v) => v.current);

  const baseNumber = Number(search.get("base")) || undefined;
  const base = useStrategyVersion(name, baseNumber);
  const runtime = useStrategyVersion(name, current?.version, true);

  // Counts the times the editor is told to start over, so it loads the newest text again.
  const [restarts, setRestarts] = useState(0);
  const [copying, setCopying] = useState(false);
  const [saved, setSaved] = useState<number | null>(null);

  async function reload() {
    await queryClient.invalidateQueries({ queryKey: strategyKeys.root(name) });
    setRestarts((n) => n + 1);
  }

  const layer = strategy.layer;
  const shadowed = layer === "file" && current !== undefined;

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">{name}</h1>
      <p>
        <StrategyLayerBadge layer={layer} /> <StrategyStatusBadge status={strategy.status} reason={strategy.statusReason} />
        {strategy.version && (
          <>
            {" "}
            Version <code>{strategy.version}</code>
          </>
        )}
      </p>
      {strategy.description && <p>{strategy.description}</p>}
      <p>
        <Link to="/strategies">Back to the strategies</Link>
      </p>

      {saved !== null && <Alert tone="success" title={`Saved version ${saved}.`} />}

      {layer === "built-in" && <BuiltInText strategy={strategy} canEdit={canEdit} copying={copying} onCopy={() => setCopying(true)} />}
      {layer === "file" && (
        <>
          <Alert tone="warning" title="This is a strategy file">
            <p>It is read from the strategies directory and cannot be changed here. A file always wins over a runtime strategy of the same name.</p>
          </Alert>
          <YamlText text={strategy.body} />
        </>
      )}

      {copying && layer === "built-in" && canEdit && (
        <StrategyEditor
          key={`copy-${restarts}`}
          name={name}
          initialBody={strategy.body}
          baseVersion={0}
          note="This starts from the built-in strategy. Saving it adds a runtime strategy that replaces the built-in one."
          onReload={() => void reload()}
          onSaved={setSaved}
        />
      )}

      {(layer === "runtime" || shadowed) && (
        <RuntimeText
          name={name}
          shadowed={shadowed}
          canEdit={canEdit}
          currentVersion={current?.version}
          baseText={baseNumber !== undefined && base.isPending ? undefined : base.data?.body}
          waitingForBase={baseNumber !== undefined && base.isPending}
          baseNumber={baseNumber}
          currentText={versions.isSuccess ? (runtime.data?.body ?? (layer === "runtime" ? strategy.body : undefined)) : undefined}
          restarts={restarts}
          onReload={() => void reload()}
          onSaved={setSaved}
        />
      )}

      {list.length > 0 && (
        <section aria-labelledby="sk-strategy-history">
          <h2 id="sk-strategy-history">History</h2>
          <VersionHistory
            name={name}
            versions={list}
            hasMore={versions.hasNextPage}
            loadingMore={versions.isFetchingNextPage}
            onLoadMore={() => void versions.fetchNextPage()}
          />
        </section>
      )}
      {versions.isError && <QueryError error={versions.error} onRetry={() => void versions.refetch()} />}
    </div>
  );
}

interface BuiltInTextProps {
  strategy: StrategyDetail;
  canEdit: boolean;
  copying: boolean;
  onCopy: () => void;
}

/** The text of a built-in strategy, with the button that starts a runtime strategy from it. */
function BuiltInText({ strategy, canEdit, copying, onCopy }: BuiltInTextProps) {
  return (
    <>
      <p>This strategy ships with Sidekick and cannot be changed. A runtime strategy with the same name replaces it.</p>
      {!copying && (
        <>
          <YamlText text={strategy.body} />
          {canEdit ? (
            <div>
              <Button variant="primary" onClick={onCopy}>
                Edit as runtime strategy
              </Button>
            </div>
          ) : (
            <p>Changing strategies needs the admin role.</p>
          )}
        </>
      )}
    </>
  );
}

interface RuntimeTextProps {
  name: string;
  shadowed: boolean;
  canEdit: boolean;
  currentVersion: number | undefined;
  baseText: string | undefined;
  waitingForBase: boolean;
  baseNumber: number | undefined;
  currentText: string | undefined;
  restarts: number;
  onReload: () => void;
  onSaved: (version: number | null) => void;
}

/** The current text of a runtime strategy: an editor for an admin, the YAML for anyone else. */
function RuntimeText({ name, shadowed, canEdit, currentVersion, baseText, waitingForBase, baseNumber, currentText, restarts, onReload, onSaved }: RuntimeTextProps) {
  const text = baseText ?? currentText;

  if (waitingForBase || text === undefined) {
    return (
      <p role="status" className="sk-loading">
        Loading
      </p>
    );
  }

  const heading = shadowed ? "Runtime strategy (not used)" : "Text";

  return (
    <section aria-labelledby="sk-strategy-text">
      <h2 id="sk-strategy-text">{heading}</h2>
      {shadowed && <p>A strategy file with this name is used instead. Versions saved here take effect if the file is removed.</p>}
      {canEdit ? (
        <StrategyEditor
          key={`${restarts}-${baseNumber ?? 0}`}
          name={name}
          initialBody={text}
          baseVersion={currentVersion ?? 0}
          note={baseNumber !== undefined ? `This starts from version ${baseNumber}. Saving it adds a new version; the older ones stay in the history.` : undefined}
          onReload={onReload}
          onSaved={onSaved}
        />
      ) : (
        <>
          <YamlText text={text} />
          <p>Changing strategies needs the admin role.</p>
        </>
      )}
    </section>
  );
}
