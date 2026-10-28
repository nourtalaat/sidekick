import { useQueryClient } from "@tanstack/react-query";
import { CircleX } from "lucide-react";
import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ApiError } from "../api/client";
import { CODE_EXISTS, CODE_VALIDATION, ROUTES_KEY, SaveError, useCreateRoute, useRoutes, useUpdateRoute, type Route } from "../api/rules";
import type { components } from "../api/schema";
import { Checkbox } from "../components/Checkbox";
import { EmptyState } from "../components/EmptyState";
import { QueryError } from "../components/QueryError";
import { Select } from "../components/Select";
import { TextArea } from "../components/TextArea";
import { TextField } from "../components/TextField";
import { hasRole } from "../roles";
import { useCurrentUser } from "../session";
import { formatLabels, joinList, orUndefined, parseDecimal, parseLabels, parseWhole, sortErrors, splitList, textOrUndefined } from "./rules/formHelpers";
import { RuleFormFrame } from "./rules/RuleFormFrame";

type RouteSpec = components["schemas"]["RouteSpec"];

/** The fields of the form, by the path the API gives them. */
const FIELDS = [
  "name",
  "priority",
  "match",
  "match.intake",
  "match.labels",
  "match.service",
  "match.severity",
  "match.text",
  "strategy",
  "sources",
  "outputs",
  "mode",
  "budgets.maxTokens",
  "budgets.maxToolCalls",
  "budgets.maxWallTime",
  "budgets.maxCostUSD",
  "grouping.window",
  "grouping.fingerprintLabels",
  "grouping.relatedWindow",
] as const;

// TODO: [SK-48] Offer approved and read-write here once the actions arrive.
const MODES = [{ value: "read-only", label: "Read-only" }];

const LIST_PATH = "/routes";

/** The priority a route has when none is given, as the config file defaults it. */
const DEFAULT_PRIORITY = "100";

interface RouteValues {
  name: string;
  priority: string;
  enabled: boolean;
  intake: string;
  labels: string;
  service: string;
  severity: string;
  text: string;
  strategy: string;
  sources: string;
  outputs: string;
  mode: string;
  maxTokens: string;
  maxToolCalls: string;
  maxWallTime: string;
  maxCostUSD: string;
  window: string;
  fingerprintLabels: string;
  relatedWindow: string;
}

const EMPTY: RouteValues = {
  name: "",
  priority: DEFAULT_PRIORITY,
  enabled: true,
  intake: "",
  labels: "",
  service: "",
  severity: "",
  text: "",
  strategy: "",
  sources: "",
  outputs: "",
  mode: "read-only",
  maxTokens: "",
  maxToolCalls: "",
  maxWallTime: "",
  maxCostUSD: "",
  window: "",
  fingerprintLabels: "",
  relatedWindow: "",
};

/** The page that adds a route (mode new) or changes a runtime one (mode edit). */
export function RouteFormPage({ mode }: { mode: "new" | "edit" }) {
  const me = useCurrentUser();
  const { name = "" } = useParams();

  if (!hasRole(me.role, "admin")) {
    return (
      <div className="sk-page">
        <QueryError error={new ApiError(403, "This needs the admin role.")} />
      </div>
    );
  }

  if (mode === "new") {
    return <RouteForm mode="new" initial={EMPTY} version={0} />;
  }

  return <EditRoute name={name} />;
}

/** Loads the runtime route named name and shows its form. */
function EditRoute({ name }: { name: string }) {
  const routes = useRoutes();

  if (routes.isPending) {
    return (
      <p role="status" className="sk-loading">
        Loading
      </p>
    );
  }

  if (routes.isError) {
    return (
      <div className="sk-page">
        <QueryError error={routes.error} onRetry={() => void routes.refetch()} />
      </div>
    );
  }

  const item = routes.data.find((r) => r.name === name && r.layer === "runtime");
  if (!item) {
    const inFile = routes.data.some((r) => r.name === name && r.layer === "file");

    return (
      <div className="sk-page">
        <EmptyState title="No route to change">
          {inFile ? `The route ${name} is set in the config file. Only a config change can alter it.` : `No route named ${name} was added at runtime.`}
        </EmptyState>
      </div>
    );
  }

  return <RouteForm key={item.version} mode="edit" initial={valuesOf(item)} version={item.version ?? 0} />;
}

interface RouteFormProps {
  mode: "new" | "edit";
  initial: RouteValues;
  /** The version that was read, which a change must send back. */
  version: number;
}

function RouteForm({ mode, initial, version }: RouteFormProps) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const create = useCreateRoute();
  const update = useUpdateRoute(initial.name);

  const [values, setValues] = useState(initial);
  const [error, setError] = useState<SaveError | null>(null);
  const [local, setLocal] = useState<Record<string, string>>({});
  const [failures, setFailures] = useState(0);

  const set = (patch: Partial<RouteValues>) => setValues((v) => ({ ...v, ...patch }));

  const sorted = sortErrors(error?.code === CODE_VALIDATION ? error.fields : [], FIELDS);
  const byField: Record<string, string | undefined> = {
    ...sorted.byField,
    ...(error?.code === CODE_EXISTS ? { name: error.message } : {}),
    ...local,
  };
  const invalid = Object.keys(byField).length > 0 || sorted.other.length > 0;

  function save() {
    const built = specOf(values, mode);
    setLocal(built.problems);

    if (!built.spec) {
      setError(null);
      setFailures((n) => n + 1);

      return;
    }

    const name = values.name.trim();
    const done = {
      onSuccess: () => void navigate(LIST_PATH, { state: { saved: `Route ${name} saved. Every replica uses it within seconds.` } }),
      onError: (e: Error) => {
        setError(e instanceof SaveError ? e : new SaveError(0, e.message, undefined, []));
        setFailures((n) => n + 1);
      },
    };

    if (mode === "new") {
      create.mutate({ name, ...built.spec }, done);
    } else {
      update.mutate({ ...built.spec, enabled: values.enabled, version }, done);
    }
  }

  function reload() {
    setError(null);
    void queryClient.invalidateQueries({ queryKey: ROUTES_KEY });
  }

  return (
    <RuleFormFrame
      title={mode === "new" ? "Add a route" : `Change route ${initial.name}`}
      listPath={LIST_PATH}
      submitLabel={mode === "new" ? "Add route" : "Save route"}
      saving={create.isPending || update.isPending}
      error={error}
      invalid={invalid}
      failures={failures}
      otherProblems={sorted.other}
      onReload={reload}
      onSubmit={save}
    >
      <fieldset className="sk-rule-form__group">
        <legend className="sk-rule-form__legend">Route</legend>
        <TextField
          label="Name"
          value={values.name}
          disabled={mode === "edit"}
          onChange={(e) => set({ name: e.target.value })}
          error={byField.name}
          help="Lower-case letters, digits and -, up to 24 characters."
          autoComplete="off"
        />
        <TextField
          label="Priority"
          inputMode="numeric"
          value={values.priority}
          onChange={(e) => set({ priority: e.target.value })}
          error={byField.priority}
          help="Routes with a lower number are tried first. The first route that matches takes the event."
        />
        {mode === "edit" && (
          <Checkbox label="In use" checked={values.enabled} onChange={(e) => set({ enabled: e.target.checked })} help="Turn it off to keep the route without using it." />
        )}
      </fieldset>

      <fieldset className="sk-rule-form__group">
        <legend className="sk-rule-form__legend">Which events</legend>
        {byField.match && (
          <p className="sk-field__error">
            <CircleX size={16} aria-hidden="true" />
            {byField.match}
          </p>
        )}
        <TextField
          label="Intake"
          value={values.intake}
          onChange={(e) => set({ intake: e.target.value })}
          error={byField["match.intake"]}
          help="Names of webhook endpoints and listeners, separated by commas."
        />
        <TextField
          label="Service"
          value={values.service}
          onChange={(e) => set({ service: e.target.value })}
          error={byField["match.service"]}
          help="A pattern such as checkout-*."
          autoComplete="off"
        />
        <TextField
          label="Severity"
          value={values.severity}
          onChange={(e) => set({ severity: e.target.value })}
          error={byField["match.severity"]}
          help="Any of unknown, info, warning and critical, separated by commas."
        />
        <TextArea
          label="Labels"
          mono
          value={values.labels}
          onChange={(e) => set({ labels: e.target.value })}
          error={byField["match.labels"]}
          help="One name=value to a line. The event must have all of them."
          spellCheck={false}
        />
        <TextField
          label="Text pattern"
          value={values.text}
          onChange={(e) => set({ text: e.target.value })}
          error={byField["match.text"]}
          help="A regular expression the event text must match."
          autoComplete="off"
          spellCheck={false}
        />
        <p className="sk-field__help">A route needs at least one of these, so it never takes every event ahead of the default route.</p>
      </fieldset>

      <fieldset className="sk-rule-form__group">
        <legend className="sk-rule-form__legend">How it investigates</legend>
        <TextField
          label="Strategy"
          value={values.strategy}
          onChange={(e) => set({ strategy: e.target.value })}
          error={byField.strategy}
          help="The name of a strategy. Empty uses the default strategy."
          autoComplete="off"
        />
        <TextField
          label="Sources"
          value={values.sources}
          onChange={(e) => set({ sources: e.target.value })}
          error={byField.sources}
          help="Names of sources from the config file, separated by commas."
        />
        <TextField
          label="Outputs"
          value={values.outputs}
          onChange={(e) => set({ outputs: e.target.value })}
          error={byField.outputs}
          help="Names of outputs from the config file, separated by commas."
        />
        <Select
          label="Mode"
          value={values.mode}
          onChange={(e) => set({ mode: e.target.value })}
          options={MODES}
          error={byField.mode}
          help="Read-only is the only mode until actions can be approved."
        />
      </fieldset>

      <fieldset className="sk-rule-form__group">
        <legend className="sk-rule-form__legend">Limits of one investigation</legend>
        <TextField
          label="Most tokens"
          inputMode="numeric"
          value={values.maxTokens}
          onChange={(e) => set({ maxTokens: e.target.value })}
          error={byField["budgets.maxTokens"]}
          help="Empty uses the global value, which a route may not exceed."
        />
        <TextField
          label="Most tool calls"
          inputMode="numeric"
          value={values.maxToolCalls}
          onChange={(e) => set({ maxToolCalls: e.target.value })}
          error={byField["budgets.maxToolCalls"]}
          help="Empty uses the global value, which a route may not exceed."
        />
        <TextField
          label="Longest run"
          value={values.maxWallTime}
          onChange={(e) => set({ maxWallTime: e.target.value })}
          error={byField["budgets.maxWallTime"]}
          help="A time such as 5m. Empty uses the global value, which a route may not exceed."
        />
        <TextField
          label="Most cost in dollars"
          inputMode="decimal"
          value={values.maxCostUSD}
          onChange={(e) => set({ maxCostUSD: e.target.value })}
          error={byField["budgets.maxCostUSD"]}
          help="For example 2.50. Empty uses the global value, which a route may not exceed."
        />
      </fieldset>

      <fieldset className="sk-rule-form__group">
        <legend className="sk-rule-form__legend">Grouping</legend>
        <TextField
          label="Window"
          value={values.window}
          onChange={(e) => set({ window: e.target.value })}
          error={byField["grouping.window"]}
          help="How long a group stays open after its newest event, such as 30m. Empty uses 30m."
        />
        <TextField
          label="Fingerprint labels"
          value={values.fingerprintLabels}
          onChange={(e) => set({ fingerprintLabels: e.target.value })}
          error={byField["grouping.fingerprintLabels"]}
          help="Labels that tell alerts apart, separated by commas. Empty uses the fingerprint of the intake."
        />
        <TextField
          label="Related window"
          value={values.relatedWindow}
          onChange={(e) => set({ relatedWindow: e.target.value })}
          error={byField["grouping.relatedWindow"]}
          help="Lets an event join an open group of a related service, such as 15m. Empty turns it off."
        />
      </fieldset>
    </RuleFormFrame>
  );
}

/** What the form builds: the parts of a route that the API takes, or nothing and the problems found. */
interface Built {
  spec?: RouteSpec;
  problems: Record<string, string>;
}

/** Builds the route from the form, and finds the problems that only the form can see: what is not a number or a label. */
function specOf(values: RouteValues, mode: "new" | "edit"): Built {
  const problems: Record<string, string> = {};

  if (mode === "new" && values.name.trim() === "") {
    problems.name = "Enter a name.";
  }

  const number = (field: string, text: string, parse: (t: string) => number | undefined | null, what: string): number | undefined => {
    const n = parse(text);
    if (n === null) {
      problems[field] = `Enter ${what}.`;

      return undefined;
    }

    return n;
  };

  const priority = number("priority", values.priority, parseWhole, "a whole number");
  const maxTokens = number("budgets.maxTokens", values.maxTokens, parseWhole, "a whole number");
  const maxToolCalls = number("budgets.maxToolCalls", values.maxToolCalls, parseWhole, "a whole number");
  const maxCostUSD = number("budgets.maxCostUSD", values.maxCostUSD, parseDecimal, "an amount such as 2.50");

  const labels = parseLabels(values.labels);
  if (labels.error) {
    problems["match.labels"] = labels.error;
  }

  if (Object.keys(problems).length > 0) {
    return { problems };
  }

  const match = {
    intake: orUndefined(splitList(values.intake)),
    service: textOrUndefined(values.service),
    severity: orUndefined(splitList(values.severity)),
    labels: Object.keys(labels.labels).length > 0 ? labels.labels : undefined,
    text: textOrUndefined(values.text),
  };
  const hasMatch = Object.values(match).some((v) => v !== undefined);

  return {
    problems,
    spec: {
      priority,
      match: hasMatch ? match : undefined,
      strategy: textOrUndefined(values.strategy),
      sources: orUndefined(splitList(values.sources)),
      outputs: orUndefined(splitList(values.outputs)),
      mode: "read-only",
      budgets: {
        maxTokens,
        maxToolCalls,
        maxWallTime: textOrUndefined(values.maxWallTime),
        maxCostUSD,
      },
      grouping: {
        window: textOrUndefined(values.window),
        fingerprintLabels: orUndefined(splitList(values.fingerprintLabels)),
        relatedWindow: textOrUndefined(values.relatedWindow),
      },
    },
  };
}

/** The form's values from a route of the API. */
function valuesOf(r: Route): RouteValues {
  return {
    name: r.name,
    priority: String(r.priority ?? DEFAULT_PRIORITY),
    enabled: r.enabled,
    intake: joinList(r.match?.intake),
    labels: formatLabels(r.match?.labels),
    service: r.match?.service ?? "",
    severity: joinList(r.match?.severity),
    text: r.match?.text ?? "",
    strategy: r.strategy ?? "",
    sources: joinList(r.sources),
    outputs: joinList(r.outputs),
    mode: r.mode ?? "read-only",
    maxTokens: r.budgets?.maxTokens === undefined ? "" : String(r.budgets.maxTokens),
    maxToolCalls: r.budgets?.maxToolCalls === undefined ? "" : String(r.budgets.maxToolCalls),
    maxWallTime: r.budgets?.maxWallTime ?? "",
    maxCostUSD: r.budgets?.maxCostUSD === undefined ? "" : String(r.budgets.maxCostUSD),
    window: r.grouping?.window ?? "",
    fingerprintLabels: joinList(r.grouping?.fingerprintLabels),
    relatedWindow: r.grouping?.relatedWindow ?? "",
  };
}
