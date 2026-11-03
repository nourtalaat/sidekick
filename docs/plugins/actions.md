# Writing an action

An action is a change Sidekick may make outside itself: a comment on a
ticket, a pull request, a note. The model never calls an action directly. It
proposes one, a person approves or rejects the proposal, and only then does
Sidekick run the action. This guide is for the code of a new action type.
Operators who only use actions read [the `actions` section of the
configuration](../configuration.md#actions).

The life of a proposal:

1. A route in `approved` mode lists some actions. The model gets a
   `propose_action` tool whose schema names exactly those. It calls the tool
   with an action, its parameters and a reason.
2. Sidekick checks the parameters against your schema and calls your
   `Validate`. A failure goes back to the model as an error. Otherwise the
   proposal is stored, written to the audit log and shown in Slack and in the
   web UI with the parameters whole.
3. An approver approves or rejects it. A proposal nobody decides expires
   after `actions.proposalTTL` (24 hours by default).
4. An approved proposal is run by a job. The job checks again that the route
   still lists the action and is not read-only, and calls `Validate` again
   with the limits as the config has them now. Then it calls your `Execute`.
5. The result, or the error, is stored on the proposal, written to the audit
   log and shown where the proposal was shown.

This guide walks through the steps for a new action type. The example in
[`examples/action-note`](../../examples/action-note/note.go) follows the
same steps, and its comments carry the step numbers used here. It writes a
note to a board of a made-up notes service. The example is built and tested
in CI, but nothing imports it, so it is not part of the `sidekick` binary.

## The interface

```go
type Action interface {
	Spec() Spec
	Validate(ctx context.Context, params json.RawMessage) error
	Execute(ctx context.Context, inv Invocation) (Result, error)
}
```

`Spec` describes the action to the model. `Validate` checks parameters
against the schema and the configured limits. `Execute` makes the change.
There is one instance per entry under `actions` in the config, built from its
section, and `Spec().Name` is the entry's name.

## Step 1: the config struct, the constructor and registration

Each action is one entry under `actions` in `sidekick.yaml`. The `type` key
picks your action; the other keys belong to your config struct.

- Give every field a `yaml` tag. Unknown keys are errors, so a typo is
  reported instead of being ignored.
- Never put a secret in the struct as a string. Use a `registry.SecretRef`
  (`{ name, key }`). Give the action credentials of its own, with the least
  rights that let it do its job, apart from the credentials of any source.
- Put what the action may touch in a `limits` block: repositories, boards,
  projects, how many things, how long. The limits are what stops a
  confused or tricked model, so be strict. Empty limits should mean nothing
  is allowed, and `Validate()` of the config should refuse them.
- `Validate() error` on the config runs without secrets and without network
  calls. Start each error with the key of its field and a colon, for example
  `limits.boards: at least one board is required`.
- Require https for a URL that carries a credential, except on a loopback
  host.
- The constructor must not call the service: a service that is down must not
  stop Sidekick from starting. It gets its name, the decoded config and
  `registry.Deps` (secrets, an HTTP client, the logger).

Register in an `init` function:

```go
func init() {
	registry.Actions.Register("note.create", registry.Typed(New))
}
```

## Step 2: the spec and the parameters schema

`Spec` gives the model a description and a JSON Schema of the parameters.
Both are shown to the model in the `propose_action` tool, next to the
actions of the same route. Say what the action does and what it may touch,
for example the allowed boards. Make the schema strict: `"type": "object"`,
`"additionalProperties": false`, the `required` fields, and a `maxLength` or
`enum` wherever you can. Sidekick checks parameters against the schema
itself, before it calls `Validate`, so a plugin cannot skip it.

## Step 3: Validate

`Validate` runs twice for every proposal: when the model proposes, and again
right before `Execute`, in case the config changed while the proposal waited.
It must be fast and make no network calls, unless the action cannot check a
limit without one.

- Check every parameter against the limits. A limit violation wraps
  `action.ErrLimit`, which tells Sidekick the proposal cannot be fixed by
  trying again.
- The error text goes back to the model (when proposing) or is stored on the
  proposal (when running), so say what is wrong and which limit it breaks,
  without secrets.
- A check that needs another service, such as validating the files of a pull
  request with a tool, does not belong in `Validate`, which runs twice. Implement
  `action.ProposalChecker` for it: `CheckProposal(ctx, params)` runs once, when the
  model proposes, after `Validate`. Its error goes back to the model as a refusal
  (cut at 2000 characters) and nothing is stored. It is cut off after 90
  seconds, and the model is told to propose again. It is not called again before
  `Execute`, so `Execute` makes the check again if the result can change.
- Call `Validate` at the start of `Execute` too. Sidekick already checked, but
  an action must not rely on its caller for its safety.

## Step 4: Execute and the idempotency key

`Execute` makes the change. It gets an `Invocation` with the parameters, the
proposal ID, the investigation ID, who approved it and an idempotency key,
`sidekick/<proposal id>`.

Sidekick can call `Execute` more than once for one proposal: a worker can
die after the change was made and before the result was saved, and the job is
then tried again. Every attempt gets the same key, so use it to make the
change once:

- Send it as the idempotency header of the service, if the service has one,
  as the example does.
- Otherwise use it in a name the service rejects when it exists: a branch
  `sidekick/<proposal id>` for a pull request, a label or an external ID on a
  ticket. Look for what the key made before you make it again.

A change that cannot be made safe to repeat should not be an action.

`Execute` gets a context that ends after 5 minutes, or when the worker shuts
down. Pass it to every call and return soon after it ends.

## Step 5: the result and errors

Return an `action.Result` with a one-line summary and links to what the
action made or changed. Sidekick shows the summary and the `http` and
`https` links with the proposal. Do not put secrets or long text in it.

The kind of error decides what Sidekick does:

- An error that wraps `action.ErrPermanent`, `action.ErrLimit` or
  `action.ErrConflict` ends the proposal as failed at once, with the error
  shown on it. Use `ErrPermanent` for a refusal that a repeat cannot change:
  a missing target, a 400 or 403 from the service. Use `ErrConflict` when the
  target changed since the proposal.
- Any other error is tried again with backoff, up to 3 attempts in all, and
  then the proposal ends as failed. Use it for timeouts, server errors and
  rate limits. For a rate limit with a short wait, wait and go on; with a
  long one, return the error.

Wrap errors with context. Error text is stored on the proposal and shown to
people, after redaction, so never put a token, a request body or the model's
parameters in it.

## Step 6: untrusted parameters

The parameters and the reason are text the model wrote after reading alerts,
logs and tickets, so an attacker may have shaped them. Sidekick stores the
parameters in one fixed form (no key twice) and shows approvers the
text exactly as they will run, with hidden characters spelled out, so what
the approver read is what your `Execute` gets. Your part:

- Treat every string as data. Never build a shell command, a query or a URL
  from it without escaping it for that target.
- Check a value against an allowlist when there is one: boards, repositories,
  projects, labels. Do not match with prefixes or patterns that a look-alike
  value can pass.
- Do not follow links or fetch URLs from parameters unless the limits name
  the hosts.
- Keep the action's reach small enough that a malicious but valid proposal is
  still acceptable to approve: limits on how much it can change, and a draft
  or reversible form of the change where the service has one.

## Step 7: registration in the binary

Add a blank import of your package to `internal/plugins/plugins.go`, which
is what links built-in plugins into the binary.

## Step 8: tests

- Mock the service's HTTP API with `internal/fakehttp`, never the real
  service. Start one fake server per test or subtest and pass its client in
  `registry.Deps.HTTP`, so parallel tests share nothing.
- Table-test `Validate` and the config's `Validate`, including values just
  inside and just outside every limit.
- Test the crash: call `Execute` twice with the same `Invocation` and check
  that the fake service made the change once. The example does this with a
  fake that keeps notes by idempotency key.
- Test each class of error: a refusal that wraps `ErrPermanent`, and a
  failure that may pass.
- Test that limits tightened after the approval make `Execute` refuse and
  call nothing.

## Step 9: user docs

Describe the action type, its keys, its limits and the permissions its
credentials need in [`docs/configuration.md`](../configuration.md), next to
the other action types, and say which routes should use `mode: approved`.

## Approval-only actions

An action that changes a running system, such as a rollback, must not run
without a person unless the operator chose that for the action. Mark it
approval-only by embedding `action.ReadWriteGate` in two places, and
nothing else:

```go
type Config struct {
	action.ReadWriteGate `yaml:",inline"`

	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// ...
}

type Action struct {
	action.ReadWriteGate

	// ...
}

func New(_ context.Context, name string, cfg Config, deps registry.Deps) (*Action, error) {
	return &Action{ReadWriteGate: cfg.ReadWriteGate /* ... */}, nil
}
```

The gate adds one key, `allowReadWrite` (default false), to the action's
config. Both places are needed:

- The config embeds it so the key is read from the file. Config validation
  asks the decoded config, and refuses a `read-write` route that lists the
  action unless the key is true. This is also the check for a route saved in
  the web UI or the API.
- The action embeds it so the built instance reports it too. Sidekick asks
  the instance when the model proposes, when a proposal runs, and at
  start-up, and the instance is all it has there. Copy the gate from the
  config in the constructor.

Do not write your own `ReadWriteAllowed` method and do not read the key
yourself. An action type that does not embed the gate does not take the key:
`allowReadWrite` is an unknown-key error for it, and it can be listed on
`read-write` routes as before.

What the operator sees: the error `action "<name>" (type <type>) is
approval-only, ...` for a `read-write` route without the key; a warning at
every start for an action that has the key and is listed on a `read-write`
route; and proposals of a gated action in a `read-write` investigation that
wait for a person when the key is missing.

To test it, make a small action type in a `_test.go` file that embeds the
gate in its config and in the action, and check three cases: a `read-write`
route is refused without the key, it loads with `allowReadWrite: true`, and
it loads in `mode: approved`.

## Changing a definition in place: snapshots and Undo

An action that edits something that already exists, such as a monitor or an
alert rule, should show the approver the change field by field, refuse
changes outside the fields the operator allowed, notice when someone else
edited the target, and be undoable. Sidekick does this for any action that
implements two more interfaces, `action.Snapshotter` and `action.Undoer`.
`internal/action/datadog` is the model to copy.

### Snapshot: read the target and compute the new definition

```go
type Snapshotter interface {
	Snapshot(ctx context.Context, params json.RawMessage) (action.Snapshot, error)
}
```

Sidekick calls `Snapshot` when the model proposes, after `Validate`. Read
the live definition and make the new one from the parameters, without
changing anything:

- `Target` names what changes, for example `datadog:monitor/123`.
- `Before` is the live definition. Keep only the fields the action may
  change or needs to write back, and leave out read-only fields such as IDs
  and timestamps.
- `After` is the new definition. It is JSON `null` when the action removes
  the target.
- `Version` is the provider's version or modification time of `Before`.

Sidekick stores the snapshot with the proposal and shows the approver the
difference, one line for each field. A snapshot that changes nothing is
refused, and so is one that differs in more than `action.MaxChanges` (100)
fields, because an approver must be able to read all of it.

Use the helpers in `internal/action`:

- `action.Apply(doc, edits)` sets fields by path, for example
  `options.thresholds.critical` or `rules[0].expr`.
- `action.Diff(before, after)` lists the fields that differ.
  Objects are compared key by key; an array is one field, except an array
  of objects of the same length, which is compared position by position.
- `action.CheckFields(allowed, deltas)` refuses, with `action.ErrLimit`, a
  change outside `limits.fields`. Parse the configured list once with
  `action.ParsePaths`.

Refuse a change the limits do not allow with an error that wraps
`action.ErrLimit`. The model reads the error and may propose again.

### limits.fields

Give the action a `limits.fields` list of the paths it may change. A field
is allowed when a listed path is the field itself or a path that the field
goes on from at a dot or bracket, so `options.thresholds` allows
`options.thresholds.critical`, but `options.thresholds.critical` does not
allow `options.thresholds.warning`, and `messages` is not allowed by
`message`. `[*]` stands for any position of an array. Check the keys of the
parameters in `Validate` (no network), and check the real difference again in
`Snapshot` and in `Execute`, because the limits may have been tightened
since. Refuse fields that are read-only in the tool when the config loads.

### Execute: write what the approver read

`Execute` gets the stored snapshot in `Invocation.Snapshot`. Write
`Snapshot.After`, never a definition computed again from the parameters.
Right before writing, read the target again and decide in this order, which
also makes a retry after a crash safe:

1. The changed fields already have their `After` values
   (`action.AtAfter(live, deltas)`): the change is made. Return success
   without writing.
2. The live definition equals `Before` and its version equals
   `Snapshot.Version`: write `After`.
3. Anything else: return an error that wraps `action.ErrConflict`. The
   proposal fails, and a new one is needed.

### Undo: restore only the fields the action changed

```go
type Undoer interface {
	Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error)
}
```

Sidekick shows an Undo button on a succeeded proposal of an action that
implements `Undoer`, and only an approver can use it: a signed-in person or
a Slack approver, never an API token. `snap` is the snapshot the proposal
stored when the action changed the target. The restore is field by field, so
a later edit by someone else to another field stays:

1. Read the live definition. If the changed fields already have their
   `Before` values (`action.AtBefore`), return success: the target is
   restored.
2. If they do not have their `After` values (`action.AtAfter`) and `force`
   is false, someone changed one of them since. Return
   `&action.ConflictError{Live: live}` and write nothing. Sidekick shows the
   approver what a forced restore would overwrite and asks again with
   `force` true.
3. Otherwise put the fields back with `action.Revert(live, deltas)` and
   write the result.

Compare only the changed fields, not the whole definition: the tool may
fill in defaults and change the timestamps, and that is not a conflict.
`Undo` must be safe to repeat, because a crash can run it twice. An action
that removes its target (`After` is `null`) cannot be undone, and Sidekick
does not offer Undo for it. An `Undoer` that has no snapshot gets the zero
`Snapshot`.

An action that creates something, such as a mute, has a snapshot whose
`Before` is `null`. Slack and the web UI show the whole `After` as the new
definition, and the Undo button says that Sidekick ends what it created. Its
`Undo` removes what `Execute` made. If someone changed it since and `force`
is false, return `&action.ConflictError{Live: live}`: the approver is shown
the live definition that a forced Undo removes.

### Tests

Add to the tests of step 8, with `internal/fakehttp`: propose a change
inside and outside the allowed fields; execute; execute when the target
changed after the snapshot; execute twice after a crash; undo; undo when
another field was edited (it stays); undo when a changed field was edited
(conflict, then forced).
