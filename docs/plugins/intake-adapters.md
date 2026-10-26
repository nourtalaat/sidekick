# Writing an intake adapter

An intake adapter reads the webhook payload of one kind of sender, such as
Prometheus Alertmanager, and turns it into Sidekick's common events. Each
webhook endpoint in `sidekick.yaml` names one adapter. Everything else
about a delivery is the same for every adapter and is done by Sidekick
itself, so an adapter stays small:

1. Sidekick reads the body, up to 1 MiB.
2. It authenticates the delivery with the endpoint's bearer token or HMAC
   signature, or with the adapter's own signature check (step 5), before
   the adapter sees the payload.
3. **The adapter parses the payload into events.**
4. Sidekick sets the intake name and the trust level from the endpoint's
   config, redacts every text, the labels and the raw payload, and stores
   each event with its key. A key the intake already has is a duplicate and
   stores nothing.

This guide walks through the steps for a new adapter. The example in
[`examples/intake-uptime`](../../examples/intake-uptime/uptime.go) follows
the same steps, and its comments carry the step numbers used here. It reads
the deliveries of a made-up uptime checker that posts one JSON object when
a check goes down or comes back up, and signs the body in its own way. The
example is built and tested in CI, but nothing imports it, so it is not
part of the `sidekick` binary. The adapters in `internal/intake/generic` and
`internal/intake/alertmanager` are the real ones.

## The interface

```go
type Adapter interface {
	Parse(ctx context.Context, req Request) ([]event.Event, error)
}
```

`Request` holds the endpoint name, the request headers, the body and the
time the delivery arrived. `Parse` returns the events in the delivery: one
for most senders, one per alert for senders such as Alertmanager that
group alerts, and none for a delivery that holds no alert.

## Step 1: the config struct and the constructor

Each webhook endpoint is one entry under `intake.webhooks`. Sidekick reads
the `adapter`, `auth` and `trusted` keys itself; the endpoint's other keys
are decoded into your adapter's config struct.

- Give every field a `yaml` tag. Unknown keys are errors, so a typo in a
  user's config is reported instead of being ignored.
- Never put a secret in the struct as a string. Use a `registry.SecretRef`
  (`{ name, key }`), which points at a file mounted from a Kubernetes
  Secret.
- Add a `Validate() error` method for rules of your own. Start each error
  with the key of its field and a colon, for example
  `service: must not start or end with spaces`, so the error is shown next
  to the right line of the config. `sidekick config validate` runs it
  without secrets or network calls.
- A field whose type implements `encoding.TextUnmarshaler` is parsed when
  the config loads. The generic adapter uses this for its JSON paths, with
  the type from `internal/jsonpath`.

Then write a constructor with this shape:

```go
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (intake.Adapter, error)
```

It gets the config already decoded and validated. `deps.Secrets` reads
secret references and `deps.Logger` is the structured logger.
`deps.Metrics` is where the adapter registers its own Prometheus metrics,
through `registry.RegisterOrExisting`, so several instances of the type
share one collector; it is nil in tests that do not set it.

## Step 2: parsing the payload

Decode the body into a struct with the fields you need, and map them to an
`event.Event`:

- `Title` is a one-line summary and `Text` the longer description.
- `AlertName`, `Service`, `Labels` and `Links` are what routes match on and
  what the investigation starts from. Fill in what the sender has.
- `Severity`: turn the sender's name into one of Sidekick's four levels
  with `intake.ParseSeverity`, which knows the common names.
- `StartsAt` and `EndsAt` are when the alert started and ended, if the
  sender says.
- `Raw` is the payload of this event, which Sidekick keeps (redacted) for
  debugging. When a delivery holds several alerts, keep the delivery's
  shape with only this event's alert in it, so the raw payload can be fed
  through the adapter again.
- `Fingerprint` groups repeated alerts. Build it with `event.Fingerprint`
  from your adapter's name, the alert's name and the labels that identify
  the alert.

Return an error for a payload you cannot read. Sidekick answers the sender
`400` with the error's text and logs it, so name the field that is wrong
but never put a value from the payload in the error.

## Step 3: the key

Every event needs a `Key` that the payload alone decides. Sidekick stores
at most one event per key and endpoint, so a sender that delivers the same
alert twice, as most senders do on a retry, creates one event.

- Use the sender's own ID for the alert when it has one, combined with
  whatever makes a new notification different: its status and its start
  time, for example.
- Do not use the time the delivery arrived, a random value, or anything
  else that changes on a retry.
- The key may be long or hold sensitive text. Sidekick stores a key that is
  longer than 256 bytes, or that the redaction rules would change, as its
  SHA-256 instead.

## Step 4: kinds, trust and redaction

- Set `Kind` to `alert`, or to `resolved` for a notification that an alert
  has cleared. Resolved events are stored, and linked to their
  investigation in a later release.
- Leave `Trust` and `Intake` alone. Trust comes from the endpoint's
  `trusted` setting, never from the payload, and Sidekick sets both.
- Do not redact anything yourself. Sidekick redacts every text, label and
  raw payload with the configured rules before it stores the event.
- Never log the payload or put it in a span attribute.

## Step 5: a signature scheme of your own (optional)

Most senders can send a bearer token, and Sidekick's own HMAC scheme
covers senders that can sign requests in a way you choose. A sender that
signs requests in its own fixed way needs its own check: implement
`intake.SignatureVerifier` on the adapter.

```go
type SignatureVerifier interface {
	VerifySignature(req Request, secret []byte) error
}
```

Sidekick then calls `VerifySignature` instead of its own check, with the
secret the endpoint's `auth` names, before `Parse`. Compare signatures with
`hmac.Equal` or `crypto/subtle`, never with `==` or `bytes.Equal`, so the
time the comparison takes does not give the signature away. Wrap
`intake.ErrUnauthenticated` into every error; Sidekick answers `401`.

## Step 6: registration

Register the adapter in the package's `init` function:

```go
func init() {
	registry.Intakes.Register("myadapter", registry.Typed(New))
}
```

`registry.Typed` decodes the endpoint's section into your struct, runs its
`Validate` method, and calls your constructor. Then add a blank import of
your package to `internal/plugins/plugins.go`, which is what links built-in
plugins into the binary.

## Step 7: tests

- Table-test `Parse` with payloads recorded from the real sender, kept in
  the package's `fixture/` directory. Include a delivery with several
  alerts if the sender groups them, a resolved alert, and payloads with
  missing fields and fields of the wrong type.
- Check that the same delivery gives the same keys twice, and that each
  thing that should make a new notification changes the key.
- Table-test `Validate`, and `VerifySignature` with a right signature, a
  wrong one and none.
- The HTTP handler, authentication, redaction and storage are tested in
  `internal/intake/webhook` against a real database; an adapter's tests
  need neither a server nor a database.

## Step 8: user docs

Describe the adapter, its keys and how to point the sender at Sidekick in
[`docs/webhooks.md`](../webhooks.md), next to the other adapters.
