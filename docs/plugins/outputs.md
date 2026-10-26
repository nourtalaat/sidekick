# Writing an output

An output shows investigations somewhere people look: a Slack thread, a
ticket comment, a webhook callback. Each route lists its outputs, and every
investigation on the route is shown in each of them. The output only ever
sees the `output.Sink` interface in `internal/output`; Sidekick's publish
job does the rest:

1. When the router creates an investigation, it writes the investigation's
   first state, "queued", and enqueues one publish job per output of the
   route.
2. The investigator writes a new state each time something changes, with a
   higher `Seq`, and enqueues the jobs again. A job that has not run yet is
   kept, not doubled.
3. **A publish job calls your sink with the newest state.** States written
   while the job waited are skipped, so a slow output shows the newest
   state rather than every one in between.
4. Sidekick stores the `Ref` your sink returns and passes it back as
   `Previous` with the next update, so your sink can edit what it published
   instead of sending something new.

Only one publish job runs at a time for one investigation and output, on
any replica, so your sink never edits the same message from two places at
once.

This guide walks through the steps for a new output. The example in
[`examples/output-board`](../../examples/output-board/board.go) follows the
same steps, and its comments carry the step numbers used here. It keeps
one card per investigation on a made-up status board with a small JSON
API. The example is built and tested in CI, but nothing imports it, so it
is not part of the `sidekick` binary. The Slack output in
`internal/output/slack` is the full version of every step.

## The interface

```go
type Sink interface {
	Accepts(phase Phase) bool
	Publish(ctx context.Context, update Update) (Ref, error)
}
```

`Accepts` says which phases the output shows. `Publish` sends or edits the
output's message for one state of an investigation, and returns where it
is.

## Step 1: the config struct, the constructor and registration

Each output is one entry under `outputs` in `sidekick.yaml`. The `type` key
picks the output; the other keys belong to your config struct.

- Give every field a `yaml` tag. Unknown keys are errors, so a typo is
  reported instead of being ignored.
- Never put a secret in the struct as a string. Use a `registry.SecretRef`
  (`{ name, key }`), which points at a file mounted from a Kubernetes
  Secret. Config validation finds every `SecretRef` in your struct by
  itself.
- Write `Validate() error` for rules of your own. It runs without secrets
  and without network calls. Start each error with the key of its field
  and a colon, for example `url: required`, and join several problems with
  `errors.Join`.

The constructor has this shape:

```go
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (output.Sink, error)
```

`deps.Secrets` reads secret references, `deps.HTTP` is the HTTP client to
send requests with (use `httpclient.New()` when it is nil), and
`deps.Logger` is the structured logger. `deps.Metrics` is where the output
registers its own Prometheus metrics, through
`registry.RegisterOrExisting`, so several instances of the type share one
collector; it is nil in tests that do not set it. An output that posts to
Slack with the shared Slack app reads the bot token from
`deps.SlackBotToken`, the reference in `slack.botTokenSecret`.

Read secrets when you use them, not once in the constructor, so a rotated
secret is used without a restart. Do not call the remote service in the
constructor: a service that is down must not stop Sidekick from starting.

Register the type in the package's `init` function:

```go
func init() {
	registry.Outputs.Register("mytype", registry.Typed(New))
}
```

## Step 2: phases

An update's `Phase` is the stage it describes: `queued` when the
investigation is created, `running` while it runs, and `final` once it has
a result. Later releases add phases for follow-up answers, action
proposals and watchers. `Accepts` returns true for the phases your output
shows; the publish job skips the others without calling `Publish`. A
webhook callback, for example, accepts only `final`; a chat message that
updates in place accepts all three, so people see that an investigation
started within seconds.

## Step 3: publishing and editing

`Publish` gets an `output.Update` with:

- `Event`, the event that opened the investigation's group, as intake
  stored it (already redacted), and `Events`, how many events the group
  has;
- `Progress`, every progress line so far, oldest first;
- `Report`, set with `final`. A final update without a report, or with
  `Report.StopReason` `error`, is a run that could not finish; show that
  too, so nothing stays on "investigating" forever;
- `Previous`, the `Ref` your sink returned last time, or nil the first
  time;
- `Seq`, which increases with every state of the investigation.

When `Previous` is nil, send a new message and return a `Ref` whose
`External` says where it is, such as a message ID. When `Previous` is set,
edit that message. Put anything else you need next time in `Ref.Data`,
such as the `Seq` the message shows or when it was last edited: Sidekick
stores the ref and any replica may handle the next update.

Updates can arrive late, for example when a job is retried. Store the
`Seq` you showed in `Ref.Data`, and when an update's `Seq` is not higher
than that, return `Previous` unchanged without sending anything. If the
service limits how often a message may be edited, wait in `Publish` until
the next edit is allowed (the Slack output waits 3 seconds between edits);
the publish job then shows the newest state on its next run.

Editing a message does not notify anyone in most chat tools. If people
should hear about the verdict, send a short new message when the final
verdict arrives, once per verdict, and record that in `Ref.Data` so a
retried final does not send it twice.

## Step 4: untrusted text

The report's text comes from the model, and the event's title and text
come from whoever sent the alert. Treat both as untrusted:

- Escape them for your target format, so they cannot add mentions, links
  or formatting. For Slack that means escaping `&`, `<` and `>`.
- Link only to evidence URLs, which come from tool results, never to URLs
  inside the model's text, and only to `http` and `https` URLs.
- Cut long text to the service's limits.

## Step 5: errors and retries

Return an error, wrapped with context, when a call fails. The publish job
retries it with backoff, about half an hour in all, and keeps the message
locked to the job meanwhile. Honor rate limits: wait for `Retry-After` on
a 429 when the wait is short, and return an error when it is long, so the
job retries later instead of holding a worker. Never log or return the
message text in an error.

## Step 6: registration in the binary

Add a blank import of your package to `internal/plugins/plugins.go`, which
is what links built-in plugins into the binary.

## Step 7: tests

- Mock the service's HTTP API with `internal/fakehttp`, never the real
  service. Start one fake server per test or subtest and pass its client
  in `registry.Deps.HTTP`, so parallel tests share nothing.
- Cover the first message, an edit, an update with an older `Seq`, the
  final update, a run that could not finish, and a failed call.
- If the output renders rich messages, keep golden files of them in
  `fixture/` with an `-update` flag to rewrite them, as the Slack output
  does, and include text that tries to add mentions or links.
- Table-test `Validate` and `Accepts`.

## Step 8: user docs

Describe the output, its keys and the permissions it needs in
[`docs/configuration.md`](../configuration.md), next to the other output
types.
