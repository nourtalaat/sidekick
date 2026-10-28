import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ApiError } from "../api/client";
import {
  CODE_EXISTS,
  CODE_VALIDATION,
  LISTENERS_KEY,
  SaveError,
  useCreateListener,
  useListeners,
  useUpdateListener,
  type Listener,
} from "../api/rules";
import { Checkbox } from "../components/Checkbox";
import { EmptyState } from "../components/EmptyState";
import { QueryError } from "../components/QueryError";
import { Select } from "../components/Select";
import { TextArea } from "../components/TextArea";
import { TextField } from "../components/TextField";
import { hasRole } from "../roles";
import { useCurrentUser } from "../session";
import { joinLines, joinList, orUndefined, sortErrors, splitLines, splitList, textOrUndefined } from "./rules/formHelpers";
import { RuleFormFrame } from "./rules/RuleFormFrame";

/** The fields of the form, by the path the API gives them. */
const FIELDS = [
  "name",
  "source",
  "channel",
  "match.sender.kind",
  "match.sender.names",
  "match.sender.bots",
  "match.sender.users",
  "match.text",
  "extract",
  "recovery",
] as const;

const SENDER_KINDS = [
  { value: "", label: "People and bots" },
  { value: "bot", label: "Bots only" },
  { value: "human", label: "People only" },
];

const SOURCES = [
  { value: "slack", label: "Slack" },
  { value: "teams", label: "Microsoft Teams" },
];

const LIST_PATH = "/listeners";

interface ListenerValues {
  name: string;
  source: string;
  channel: string;
  senderKind: string;
  senderNames: string;
  senderBots: string;
  senderUsers: string;
  text: string;
  extract: string;
  recovery: string;
  enabled: boolean;
}

const EMPTY: ListenerValues = {
  name: "",
  source: "slack",
  channel: "",
  senderKind: "",
  senderNames: "",
  senderBots: "",
  senderUsers: "",
  text: "",
  extract: "",
  recovery: "",
  enabled: true,
};

/** The page that adds a listener (mode new) or changes a runtime one (mode edit). */
export function ListenerFormPage({ mode }: { mode: "new" | "edit" }) {
  const me = useCurrentUser();
  const { name = "" } = useParams();

  if (!hasRole(me.role, "responder")) {
    return (
      <div className="sk-page">
        <QueryError error={new ApiError(403, "This needs the responder role.")} />
      </div>
    );
  }

  if (mode === "new") {
    return <ListenerForm mode="new" initial={EMPTY} version={0} />;
  }

  return <EditListener name={name} />;
}

/** Loads the runtime listener named name and shows its form. */
function EditListener({ name }: { name: string }) {
  const listeners = useListeners();

  if (listeners.isPending) {
    return (
      <p role="status" className="sk-loading">
        Loading
      </p>
    );
  }

  if (listeners.isError) {
    return (
      <div className="sk-page">
        <QueryError error={listeners.error} onRetry={() => void listeners.refetch()} />
      </div>
    );
  }

  const item = listeners.data.find((l) => l.name === name && l.layer === "runtime");
  if (!item) {
    const inFile = listeners.data.some((l) => l.name === name && l.layer === "file");

    return (
      <div className="sk-page">
        <EmptyState title="No listener to change">
          {inFile ? `The listener ${name} is set in the config file. Only a config change can alter it.` : `No listener named ${name} was added at runtime.`}
        </EmptyState>
      </div>
    );
  }

  return <ListenerForm key={item.version} mode="edit" initial={valuesOf(item)} version={item.version ?? 0} />;
}

interface ListenerFormProps {
  mode: "new" | "edit";
  initial: ListenerValues;
  /** The version that was read, which a change must send back. */
  version: number;
}

function ListenerForm({ mode, initial, version }: ListenerFormProps) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const create = useCreateListener();
  const update = useUpdateListener(initial.name);

  const [values, setValues] = useState(initial);
  const [error, setError] = useState<SaveError | null>(null);
  const [local, setLocal] = useState<Record<string, string>>({});
  const [failures, setFailures] = useState(0);

  const set = (patch: Partial<ListenerValues>) => setValues((v) => ({ ...v, ...patch }));

  const sorted = sortErrors(error?.code === CODE_VALIDATION ? error.fields : [], FIELDS);
  const byField: Record<string, string | undefined> = {
    ...sorted.byField,
    ...(error?.code === CODE_EXISTS ? { name: error.message } : {}),
    ...local,
  };
  const invalid = Object.keys(byField).length > 0 || sorted.other.length > 0;

  function save() {
    const problems = checkValues(values, mode);
    setLocal(problems);

    if (Object.keys(problems).length > 0) {
      setError(null);
      setFailures((n) => n + 1);

      return;
    }

    const name = values.name.trim();
    const done = {
      onSuccess: () => void navigate(LIST_PATH, { state: { saved: `Listener ${name} saved. Every replica uses it within seconds.` } }),
      onError: (e: Error) => {
        setError(e instanceof SaveError ? e : new SaveError(0, e.message, undefined, []));
        setFailures((n) => n + 1);
      },
    };

    if (mode === "new") {
      create.mutate({ name, ...specOf(values) }, done);
    } else {
      update.mutate({ ...specOf(values), enabled: values.enabled, version }, done);
    }
  }

  function reload() {
    setError(null);
    void queryClient.invalidateQueries({ queryKey: LISTENERS_KEY });
  }

  const teams = values.source === "teams";

  return (
    <RuleFormFrame
      title={mode === "new" ? "Add a listener" : `Change listener ${initial.name}`}
      listPath={LIST_PATH}
      submitLabel={mode === "new" ? "Add listener" : "Save listener"}
      saving={create.isPending || update.isPending}
      error={error}
      invalid={invalid}
      failures={failures}
      otherProblems={sorted.other}
      onReload={reload}
      onSubmit={save}
    >
      <fieldset className="sk-rule-form__group">
        <legend className="sk-rule-form__legend">Channel</legend>
        <TextField
          label="Name"
          value={values.name}
          disabled={mode === "edit"}
          onChange={(e) => set({ name: e.target.value })}
          error={byField.name}
          help="Routes pick this listener's events with match.intake and this name. Lower-case letters, digits and -."
          autoComplete="off"
        />
        <Select label="Chat app" value={values.source} onChange={(e) => set({ source: e.target.value })} options={SOURCES} error={byField.source} />
        <TextField
          label="Channel"
          value={values.channel}
          onChange={(e) => set({ channel: e.target.value })}
          error={byField.channel}
          help={teams ? "A Teams channel ID such as 19:abc123@thread.tacv2." : 'A channel name such as "#alerts" or a channel ID such as C01ABCDEF.'}
          autoComplete="off"
        />
      </fieldset>

      <fieldset className="sk-rule-form__group">
        <legend className="sk-rule-form__legend">Which messages</legend>
        <Select
          label="Sent by"
          value={values.senderKind}
          onChange={(e) => set({ senderKind: e.target.value })}
          options={SENDER_KINDS}
          error={byField["match.sender.kind"]}
        />
        <TextField
          label="Bot names"
          value={values.senderNames}
          onChange={(e) => set({ senderNames: e.target.value })}
          error={byField["match.sender.names"]}
          help="Separate with commas, for example Grafana, SigNoz. Leave all three sender lists empty to take every sender."
        />
        <TextField
          label="Bot IDs"
          value={values.senderBots}
          onChange={(e) => set({ senderBots: e.target.value })}
          error={byField["match.sender.bots"]}
          help={teams ? 'Separate with commas. A Teams bot ID starts with "28:".' : "Separate with commas, for example B01ABCDEF."}
        />
        <TextField
          label="User IDs"
          value={values.senderUsers}
          onChange={(e) => set({ senderUsers: e.target.value })}
          error={byField["match.sender.users"]}
          help={teams ? "Separate with commas. Microsoft Entra object IDs." : "Separate with commas, for example U01ABCDEF."}
        />
        <TextField
          label="Message pattern"
          value={values.text}
          onChange={(e) => set({ text: e.target.value })}
          error={byField["match.text"]}
          help="A regular expression the message text must match. Leave empty to take every message."
          autoComplete="off"
          spellCheck={false}
        />
      </fieldset>

      <fieldset className="sk-rule-form__group">
        <legend className="sk-rule-form__legend">Reading the message</legend>
        <TextArea
          label="Extract patterns"
          mono
          value={values.extract}
          onChange={(e) => set({ extract: e.target.value })}
          error={byField.extract}
          help="One regular expression to a line. Named groups set the alert name, service, severity or a label, for example (?P<service>[a-z-]+). The first pattern that sets a field wins."
          spellCheck={false}
        />
        <TextField
          label="Recovery pattern"
          value={values.recovery}
          onChange={(e) => set({ recovery: e.target.value })}
          error={byField.recovery}
          help="A regular expression. A message it matches is a recovery, not an alert."
          autoComplete="off"
          spellCheck={false}
        />
        {mode === "edit" && (
          <Checkbox label="In use" checked={values.enabled} onChange={(e) => set({ enabled: e.target.checked })} help="Turn it off to keep the listener without using it." />
        )}
      </fieldset>
    </RuleFormFrame>
  );
}

/** The problems the form finds itself: what must be filled in. */
function checkValues(values: ListenerValues, mode: "new" | "edit"): Record<string, string> {
  const problems: Record<string, string> = {};

  if (mode === "new" && values.name.trim() === "") {
    problems.name = "Enter a name.";
  }

  if (values.channel.trim() === "") {
    problems.channel = "Enter a channel.";
  }

  return problems;
}

/** The parts of a listener that the API takes, from the form. */
function specOf(values: ListenerValues) {
  const sender = {
    kind: textOrUndefined(values.senderKind),
    names: orUndefined(splitList(values.senderNames)),
    bots: orUndefined(splitList(values.senderBots)),
    users: orUndefined(splitList(values.senderUsers)),
  };
  const hasSender = Object.values(sender).some((v) => v !== undefined);
  const text = textOrUndefined(values.text);

  return {
    source: values.source === "teams" ? "teams" : undefined,
    channel: values.channel.trim(),
    match: hasSender || text !== undefined ? { sender: hasSender ? sender : undefined, text } : undefined,
    extract: orUndefined(splitLines(values.extract)),
    recovery: textOrUndefined(values.recovery),
  };
}

/** The form's values from a listener of the API. */
function valuesOf(l: Listener): ListenerValues {
  return {
    name: l.name,
    source: l.source === "teams" ? "teams" : "slack",
    channel: l.channel,
    senderKind: l.match?.sender?.kind ?? "",
    senderNames: joinList(l.match?.sender?.names),
    senderBots: joinList(l.match?.sender?.bots),
    senderUsers: joinList(l.match?.sender?.users),
    text: l.match?.text ?? "",
    extract: joinLines(l.extract),
    recovery: l.recovery ?? "",
    enabled: l.enabled,
  };
}
