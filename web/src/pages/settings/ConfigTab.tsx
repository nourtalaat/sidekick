import { useSearchParams } from "react-router";
import { useConfig, type EffectiveConfig } from "../../api/config";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import { TextField } from "../../components/TextField";
import { ConfigCard } from "./ConfigCard";
import { filterEntries, filterRules } from "./configFilter";
import { RuntimeRulesCard } from "./RuntimeRulesCard";
import "./SettingsPage.css";

/** The page of the config file in the repository, where each key is described. */
const CONFIG_DOCS_URL = "https://github.com/nourtalaat/sidekick/blob/main/docs/configuration.md";

const FILTER_PARAM = "q";
const MAX_FILTER_CHARS = 200;

/** The number of cards that always exist besides the keys of the file: listeners and routes. */
const RUNTIME_CARDS = 2;

/**
 * The config Sidekick runs with, read-only: one card for each key of the
 * config file, and the listeners and routes added at runtime. A text filter,
 * kept in the address as q, narrows the cards and the values in them.
 */
export function ConfigTab() {
  const config = useConfig();
  const [params, setParams] = useSearchParams();
  const text = params.get(FILTER_PARAM) ?? "";

  function setFilter(next: string) {
    setParams(
      (current) => {
        const updated = new URLSearchParams(current);
        if (next === "") {
          updated.delete(FILTER_PARAM);
        } else {
          updated.set(FILTER_PARAM, next);
        }

        return updated;
      },
      { replace: true },
    );
  }

  return (
    <>
      <p className="sk-config__note">
        This is the config Sidekick runs with. Change it in the config file or the Helm values; it cannot be edited here.{" "}
        <a href={CONFIG_DOCS_URL}>Read about the config file</a>.
      </p>
      <div className="sk-config__filter">
        <TextField
          label="Filter the config"
          value={text}
          onChange={(e) => setFilter(e.target.value)}
          autoComplete="off"
          maxLength={MAX_FILTER_CHARS}
          help="Shows the keys and values that contain this text, ignoring case."
        />
      </div>
      {config.isPending && (
        <p role="status" className="sk-loading">
          Loading
        </p>
      )}
      {config.isError && <QueryError error={config.error} onRetry={() => void config.refetch()} />}
      {config.isSuccess && <ConfigSections config={config.data} needle={text.trim()} />}
    </>
  );
}

function ConfigSections({ config, needle }: { config: EffectiveConfig; needle: string }) {
  const filtering = needle !== "";
  const fileEntries = filterEntries(config.file, needle);
  const listeners = filterRules(config.runtime.listeners, needle);
  const routes = filterRules(config.runtime.routes, needle);

  const total = Object.keys(config.file).length + RUNTIME_CARDS;
  const shown = fileEntries.length + (listeners.length > 0 ? 1 : 0) + (routes.length > 0 ? 1 : 0);

  if (filtering && shown === 0) {
    return (
      <>
        <p role="status" className="sk-config__note">{`0 of ${total} sections match.`}</p>
        <EmptyState title="Nothing matches">No key or value of the config contains this text.</EmptyState>
      </>
    );
  }

  return (
    <>
      {filtering && (
        <p role="status" className="sk-config__note">
          {`${shown} of ${total} sections match.`}
        </p>
      )}
      {(!filtering || fileEntries.length > 0) && (
        <section className="sk-config__section">
          <h2 className="sk-config__heading">File settings</h2>
          {fileEntries.length === 0 ? (
            <p className="sk-config__muted">The config file sets nothing.</p>
          ) : (
            fileEntries.map(([name, value]) => <ConfigCard key={`${name}-${filtering}`} name={name} value={value} whole={config.file[name]} open={filtering} />)
          )}
        </section>
      )}
      {(!filtering || listeners.length > 0 || routes.length > 0) && (
        <section className="sk-config__section">
          <h2 className="sk-config__heading">Added at runtime</h2>
          {(!filtering || listeners.length > 0) && (
            <RuntimeRulesCard title="Listeners" rules={listeners} to="/listeners" linkLabel="Change listeners" empty="No listeners were added at runtime." />
          )}
          {(!filtering || routes.length > 0) && <RuntimeRulesCard title="Routes" rules={routes} to="/routes" linkLabel="Change routes" empty="No routes were added at runtime." />}
        </section>
      )}
    </>
  );
}
