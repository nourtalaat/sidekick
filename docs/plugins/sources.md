# Writing a source

A source gives investigations tools to call: an MCP server, a native
integration such as Kubernetes or GitHub, or Sidekick's own memory. The
investigator only ever sees the `source.Source` interface in
`internal/source`, and every call goes through the guard in
`internal/investigator/tools`, so a new source gets filtering, limits and
failure handling without writing them itself.

This guide walks through the steps for a new source. The example in
[`examples/source-hello`](../../examples/source-hello/hello.go) follows the
same steps, and its comments carry the step numbers used here. It has one
tool that greets a person by name, so you can read the shape of a source
without a real service in the way. The example is built and tested in CI,
but nothing imports it, so it is not part of the `sidekick` binary. The MCP
source in `internal/source/mcp` is the full version of every step,
including a connection that is made in the background.

## The interface

```go
type Source interface {
	Tools(ctx context.Context) ([]Tool, error)
	Call(ctx context.Context, call Call) (Result, error)
	Close() error
}
```

`Tools` lists every tool the source has, before any filtering. `Call` runs
one of them. `Close` releases connections and child processes when
Sidekick stops.

## What the guard does for you

You do not write any of this in a source:

- **Filtering.** The `tools.allow` and `tools.deny` globs, then the
  read-only rule, then `tools.rename`, then the qualified name
  `<source>__<tool>`, which must fit 64 characters and use only letters,
  digits, `_` and `-`. Every tool left out is logged once, with the reason.
- **A fixed list per investigation.** Each investigation copies the tools
  when it starts, so they never change while it runs.
- **Input checks.** The model's input is checked against the tool's JSON
  Schema before `Call` sees it. A tool whose schema cannot be read is left
  out.
- **Limits.** Each call gets the source's `timeout` (default 30 seconds),
  and at most `concurrency` calls (default 4) run at once on one replica.
  A call that waits too long for a free slot times out too.
- **A circuit breaker.** After 5 failures in a row the source is skipped
  for 60 seconds and calls fail at once; then one trial call decides
  whether it is back.
- **Panics** in `Tools` or `Call` become tool errors.
- **Redaction and untrusted wrapping** of every result, evidence title and
  error message, before the model sees it.
- **The checked list** in the report: every source called, with its
  status (`ok`, `no_data`, `failed` or `unavailable`), and every source
  that was unavailable when the investigation started.
- **Metrics and traces:** `sidekick_tool_calls_total`,
  `sidekick_tool_call_duration_seconds`, `sidekick_source_up` and the
  `tool.call` span.

## Step 1: the config struct, the constructor and registration

Each source instance is one entry under `sources` in `sidekick.yaml`. The
`type` key picks the source; the other keys belong to the source's own
config struct.

- Embed `source.Settings` with the `yaml:",inline"` tag. That gives every
  source the same `tools` (`allow`, `deny`, `rename`, `trustAnnotations`),
  `timeout` and `concurrency` keys, and lets `sidekick config validate`
  check them, including that renamed tools fit 64 characters.
- Add the source's own keys next to it, such as a URL or a namespace list.
- Never put a secret in the struct as a string. Use a `registry.SecretRef`
  (`{ name, key }`), which points at a file mounted from a Kubernetes
  Secret. Config validation finds every `SecretRef` in your struct by
  itself, wherever it is.
- Write `Validate() error` for rules of your own. It runs without secrets
  and without network calls. Start each error with the key of its field
  and a colon, for example `url: required`, and join several problems with
  `errors.Join`.

The constructor has this shape:

```go
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error)
```

`deps.Secrets` reads secret references, `deps.HTTP` is the HTTP client to
send requests with, and `deps.Logger` is the structured logger. Register
the type in the package's `init` function:

```go
func init() {
	registry.Sources.Register("mytype", registry.Typed(New))
}
```

**Never wait for a remote service in the constructor.** A source that is
down must block neither start-up nor readiness. Connect in the background
and retry with backoff, as the MCP source does. Until the source can take
calls, return an error that wraps `source.ErrUnavailable` from `Tools` and
`Call`; the guard then reports it as unavailable rather than failed. If
the source can tell whether it is connected, give it an `Up() bool`
method, and `sidekick_source_up` will show it.

## Step 2: tools and calls

- **Tools.** Give each tool a clear description and a JSON Schema object
  for its input. Set `ReadOnly` to false on any tool that can change
  something: the guard never shows such tools to the model, whatever
  `tools.allow` says. Changes happen only through actions. A source whose
  service marks its own tools, as MCP servers do with annotations, honors
  `tools.trustAnnotations`; the MCP rule is that a tool with annotations is
  read-only only when they say `readOnlyHint: true` and not
  `destructiveHint: true`, and a tool with no annotations is kept, so for
  it only the allowlist decides.
- **Results.** Put readable text in `Text` and JSON in `Structured`. A
  tool that ran and found nothing returns `NoData: true`, not an error.
  Put links a person can open to check a claim, such as a log query URL,
  in `Evidence`; the model never writes URLs itself, so these are the only
  links a report can show. Cut large results and set `Truncated`.
- **Errors.** When the tool ran but reported an error of its own, such as
  a query the service could not parse, return a `*source.ToolError` with
  its message. The model sees it as a tool error, and it does not count
  against the source in the circuit breaker. Return any other error,
  wrapped with context, for real failures.
- **Timeouts.** Honor the context. `call.Deadline` is the same deadline.
  A source that ignores it still times out for the model, but keeps its
  concurrency slot until it returns.
- Everything a source returns is untrusted. Do not try to clean it of
  instructions; the guard redacts it and marks it as data.

## Step 3: permissions

Use the smallest set of permissions the tools need, read-only wherever the
service allows it, and list them in the source's user docs (step 6).

## Step 4: tests

- Mock the service's HTTP API with `internal/fakehttp`, never the real
  service. Start one fake server per test or subtest and pass its client
  in `registry.Deps.HTTP`, so parallel tests share nothing. The MCP source
  tests serve a real in-process MCP server from the go-sdk through
  `fakehttp`'s `HandleFunc`.
- Cover each tool's result, an empty result (`NoData`), a tool error, a
  timeout, an oversized result and a service that is down.
- Table-test `Validate`.
- A test that runs the source through `tools.New` and `Run.Call`, as the
  example does, shows what the model will see.

## Step 5: registration in the binary

Add a blank import of your package to `internal/plugins/plugins.go`, which
is what links built-in plugins into the binary.

## Step 6: user docs

Describe the source's keys, its tools and the permissions it needs in
[`docs/configuration.md`](../configuration.md), next to the other source
types.
