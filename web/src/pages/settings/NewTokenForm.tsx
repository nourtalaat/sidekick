import { useEffect, useRef, useState, type FormEvent } from "react";
import { useCreateToken } from "../../api/tokens";
import { Alert } from "../../components/Alert";
import { Button } from "../../components/Button";
import { Select } from "../../components/Select";
import { TextField } from "../../components/TextField";
import type { Role } from "../../roles";
import { useCurrentUser } from "../../session";
import { saveErrorOf } from "../lessons/ProblemAlert";
import { sortErrors } from "../rules/formHelpers";
import { checkTokenForm, dateLimits, DEFAULT_EXPIRY_DAYS, EXPIRY_DATE_CHOICE, EXPIRY_PRESET_DAYS, ROLE_LABELS, rolesUpTo, type TokenFormErrors } from "./tokenForm";
import "./TokensTab.css";

const FIELDS = ["name", "role", "expiresAt"] as const;

const EXPIRY_OPTIONS = [
  ...EXPIRY_PRESET_DAYS.map((days) => ({ value: String(days), label: `In ${days} days` })),
  { value: EXPIRY_DATE_CHOICE, label: "On a date" },
];

interface NewTokenFormProps {
  /** Called when a token is about to be created, so the page can take the secret of the last one away. */
  onStart(): void;
  /** Called with the name and the secret of the token that was made. */
  onCreated(name: string, secret: string): void;
}

/**
 * The form that makes an API token: a name, a role up to the signed-in
 * user's own and an expiry of at most a year. The fields are checked here
 * before anything is sent; problems the server finds come back under the
 * field they belong to, and any other problem in an alert above the form.
 */
export function NewTokenForm({ onStart, onCreated }: NewTokenFormProps) {
  const me = useCurrentUser();
  const create = useCreateToken();
  const alertRef = useRef<HTMLDivElement>(null);

  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [expiry, setExpiry] = useState(String(DEFAULT_EXPIRY_DAYS));
  const [date, setDate] = useState("");
  const [checked, setChecked] = useState<TokenFormErrors>({});

  const roles = rolesUpTo(me.role);
  const failure = create.error ? saveErrorOf(create.error) : null;
  const sorted = sortErrors(failure?.fields ?? [], FIELDS);
  const errors: TokenFormErrors = { name: checked.name ?? sorted.byField["name"], role: checked.role ?? sorted.byField["role"], expiresAt: checked.expiresAt ?? sorted.byField["expiresAt"] };
  const showAlert = failure !== null && (sorted.other.length > 0 || Object.keys(sorted.byField).length === 0);
  const picksDate = expiry === EXPIRY_DATE_CHOICE;
  const limits = dateLimits(new Date());

  useEffect(() => {
    if (showAlert) {
      alertRef.current?.focus();
    }
  }, [showAlert, failure]);

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    create.reset();

    const result = checkTokenForm({ name, role, expiry, date }, roles, new Date());
    if ("errors" in result) {
      setChecked(result.errors);

      return;
    }

    setChecked({});
    onStart();
    create.mutate(
      { body: result.body, onSecret: (secret) => onCreated(result.body.name, secret) },
      {
        onSuccess: () => {
          setName("");
          setDate("");
        },
      },
    );
  }

  return (
    <form className="sk-tokens__form" onSubmit={onSubmit} aria-label="Create an API token" noValidate>
      {showAlert && (
        <Alert ref={alertRef} tone="danger" title="Could not create the token">
          {sorted.other.length > 0 ? sorted.other.map((message) => <p key={message}>{message}</p>) : <p>{failure.message}</p>}
        </Alert>
      )}
      <div className="sk-tokens__fields">
        <TextField label="Name" value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" help="What the token is for, 1 to 100 characters." error={errors.name} />
        <Select
          label="Role"
          value={role}
          onChange={(e) => setRole(e.target.value as Role)}
          options={roles.map((value) => ({ value, label: ROLE_LABELS[value] }))}
          help="Up to your own role."
          error={errors.role}
        />
        <Select label="Expires" value={expiry} onChange={(e) => setExpiry(e.target.value)} options={EXPIRY_OPTIONS} help="At most a year from now." error={picksDate ? undefined : errors.expiresAt} />
        {picksDate && (
          <TextField
            label="Last day the token works"
            type="date"
            value={date}
            min={limits.min}
            max={limits.max}
            onChange={(e) => setDate(e.target.value)}
            help={`The token stops working at the end of that day. The latest day is ${limits.max}.`}
            error={errors.expiresAt}
          />
        )}
      </div>
      <p className="sk-tokens__help">The token has the rights of its role on the API. It needs no CSRF token. It can never approve an action proposal. It is shown only once.</p>
      <div>
        <Button type="submit" variant="primary" disabled={create.isPending}>
          Create token
        </Button>
      </div>
    </form>
  );
}
