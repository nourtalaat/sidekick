# Authenticating MCP sources

An MCP source proves who it is to its server with static values that you
keep in Secrets: a header such as `Authorization: Bearer <token>` for the
`http` and `sse` transports, or an environment variable for `stdio`.
Sidekick does not support OAuth for MCP sources yet, and
`sidekick config validate` rejects an `oauth` key on an MCP source:

```text
sources.sentry.oauth: MCP sources do not support OAuth yet; put a token in a Secret and send it with headers (or env for stdio), see docs/sources/mcp-authentication.md
```

This page explains why, and shows the headers to use for common servers.

## Why there is no OAuth yet

- **Most OAuth flows need a person.** The flow that MCP servers offer for
  OAuth sends someone to a browser to sign in and approve access. Sidekick
  runs as a service with no browser and nobody at the keyboard, so it
  cannot complete that flow on its own.
- **The flow for services is not ready.** The OAuth flow meant for
  services, client credentials, is an optional extension of the MCP
  authorization spec, not part of the core spec. The client in the
  official Go SDK that Sidekick uses (`github.com/modelcontextprotocol/go-sdk`
  v1.8.0, for MCP spec 2026-07-28) marks its OAuth support as experimental.
  Its client credentials support stops working when the first token
  expires, is not safe when several requests are rejected at once, and
  turns away some common identity providers such as Microsoft Entra ID.
  The first two are fixed in the SDK but not yet released. It also offers
  OAuth only for the `http` transport, not for `sse`.
- **The servers do not need it.** Every common MCP server below accepts a
  static token, and none of them offers the client credentials flow.

Sidekick will look at OAuth again once an SDK release fixes these problems,
or once a server that Sidekick users need accepts nothing but OAuth.

## How static headers work

Each header is either a plain value or a secret with an optional prefix:

```yaml
sources:
  linear:
    type: mcp
    url: https://mcp.linear.app/mcp/readonly
    headers:
      Authorization: { secret: { name: linear-mcp, key: apiKey }, prefix: "Bearer " }
```

- Put the token in a Kubernetes Secret (or a Docker or Podman secret) and
  name it with `{ name, key }`, as for every other secret in the
  [configuration](../configuration.md). Never put a token in `value`: the
  config file is not a secret.
- The `prefix` is added in front of the secret's text, so the Secret holds
  only the token itself. A trailing newline in the Secret is removed.
- Sidekick reads the secret again on every request. To rotate a token,
  change the Secret; Sidekick uses the new value without a restart.
- Give each token the fewest permissions that the tools you allow need,
  ideally read-only access through a service account rather than a
  person's account.
- For `stdio`, use `env` in the same form instead of `headers`, and pass
  the token the way the server expects it, for example as a variable or a
  command flag that reads one.

## Examples

### SigNoz

SigNoz Cloud and the self-hosted SigNoz MCP server both accept an API key
in the `SIGNOZ-API-KEY` header. Create it under **Settings > Service
Accounts** (only admins can). The Cloud server also needs your SigNoz
instance URL in `X-SigNoz-URL`. [The SigNoz source](signoz.md) has the rest.

```yaml
sources:
  signoz:
    type: mcp
    url: https://mcp.<region>.signoz.cloud/mcp
    headers:
      SIGNOZ-API-KEY: { secret: { name: signoz-mcp, key: apiKey } }
      X-SigNoz-URL: { value: https://<your-instance>.signoz.cloud }
```

### Datadog

Datadog's MCP server accepts a personal or service access token as a bearer
token; use a service access token for Sidekick. Use the endpoint for your
Datadog site; the one below is for US1, and
[The Datadog source](datadog.md) lists the others. The service account
needs the `mcp_read` permission, and an admin must turn on MCP access for
the organization.

```yaml
sources:
  datadog:
    type: mcp
    url: https://mcp.datadoghq.com/v1/mcp
    headers:
      Authorization: { secret: { name: datadog-mcp, key: token }, prefix: "Bearer " }
```

It also accepts an API key and an application key from a service account
instead, in the `DD_API_KEY` and `DD_APPLICATION_KEY` headers.

### Sentry

The hosted Sentry server takes a Sentry user auth token with the prefix
`Sentry-Bearer`, not `Bearer`, which it keeps for OAuth tokens.

```yaml
sources:
  sentry:
    type: mcp
    url: https://mcp.sentry.dev/mcp?skills=inspect
    headers:
      Authorization: { secret: { name: sentry-mcp, key: token }, prefix: "Sentry-Bearer " }
```

For self-hosted Sentry, run `@sentry/mcp-server` with `--access-token` and
`--host` in a sidecar that serves it over HTTP, and point a source with
`transport: http` at the sidecar. [The Sentry source](sentry.md) has the
scopes and the rest.

### Linear

Linear's server accepts a Linear API key, or an OAuth token you got some
other way, as a bearer token. The `/mcp/readonly` path makes the server
list only tools that read. [The Linear source](linear.md) has the details.

```yaml
sources:
  linear:
    type: mcp
    url: https://mcp.linear.app/mcp/readonly
    headers:
      Authorization: { secret: { name: linear-mcp, key: apiKey }, prefix: "Bearer " }
```

### GitHub

GitHub's remote server accepts a personal access token as a bearer token.
A fine-grained token with read-only permissions on the repositories
Sidekick should see is the safest choice. The `/readonly` path makes the
server list only tools that read.

```yaml
sources:
  github:
    type: mcp
    url: https://api.githubcopilot.com/mcp/readonly
    headers:
      Authorization: { secret: { name: github-mcp, key: token }, prefix: "Bearer " }
```

## An in-house server behind Okta, Entra ID or another identity provider

When your own MCP server sits behind an identity provider, Sidekick still
sends a token from a Secret; something else keeps that token valid.

- **If the server accepts a long-lived token** for a service account, such
  as an API key, put it in a Secret and rotate it there on your usual
  schedule.
- **If the server accepts only short-lived access tokens**, register a
  client for Sidekick with your identity provider and run a small job, for
  example a Kubernetes CronJob, that gets a new token with the client
  credentials flow and writes it into the Secret. Run it well before the
  token expires, for example every time half of its lifetime has passed:
  Kubernetes can take a minute or more to update a mounted Secret, and
  Sidekick reads the new value on its next request.
- Ask for a token for your MCP server only (the `resource` or audience your
  provider uses for it), with read-only scopes, so a leaked token can do
  little.
