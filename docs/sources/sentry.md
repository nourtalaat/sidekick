# The Sentry source

Sentry needs no source type of its own: Sidekick reaches it through the
Sentry MCP server, with a source of `type: mcp`. This lets an investigation
look at the issues, errors, logs, traces and metrics of your projects, with
read-only tools. This page shows how to reach the server, the access the
token needs, and the config to use. The keys every MCP source takes are in
[Configuration](../configuration.md#mcp-sources-type-mcp), and how an MCP
source proves who it is is in
[Authenticating MCP sources](mcp-authentication.md).

## Reaching the server

Pick one.

- **sentry.io.** Use the hosted server, `https://mcp.sentry.dev/mcp`. Add
  `?skills=inspect` to the URL: the hosted server then offers only the
  skill that views data, and no tool that resolves issues or changes
  projects.
- **Self-hosted Sentry.** Run the stdio server `@sentry/mcp-server` in a
  [sidecar behind a bridge](../install.md#mcp-servers-in-sidecars), with
  the address of your Sentry and a token, and point a source with
  `transport: http` at the sidecar. Pin a version. The server's command is:

  ```sh
  npx @sentry/mcp-server@<version> --access-token="$SENTRY_ACCESS_TOKEN" \
    --host=sentry.example.com --skills=inspect
  ```

The search tools (`search_errors`, `search_logs`, `search_traces`,
`search_metrics` and `search_issues`) turn a question in plain words into a
Sentry query with a language model. The hosted server does that itself. A
self-hosted server needs the key of a model provider (`OPENAI_API_KEY`,
`ANTHROPIC_API_KEY` or `OPENROUTER_API_KEY`, with `EMBEDDED_AGENT_PROVIDER`
set) for them, and leaves them out without one.

## Access

Create a user auth token for Sidekick in the settings of a Sentry account
that can see the projects Sidekick should read, with only these scopes:
`org:read`, `project:read`, `team:read` and `event:read`. Keep it in a
Secret:

```sh
kubectl create secret generic sentry-mcp --from-literal=token=<token>
```

The hosted server takes the token in an `Authorization` header with the
prefix `Sentry-Bearer`, not `Bearer`, which it keeps for its own OAuth
tokens.

## Config

```yaml
sources:
  sentry:
    type: mcp
    url: https://mcp.sentry.dev/mcp?skills=inspect
    headers:
      Authorization: { secret: { name: sentry-mcp, key: token }, prefix: "Sentry-Bearer " }
    tools:
      allow:
        - find_organizations
        - find_projects
        - search_issues
        - search_errors
        - search_logs
        - search_traces
        - search_metrics
        - get_sentry_resource
```

Without `skills=inspect` the server also offers `update_issue`,
`analyze_issue_with_seer`, and the `search_sentry_tools` and
`execute_sentry_tool` pair, which reaches every other tool the token's
scopes allow, writing ones included. None of them is in `tools.allow`, so
the model never sees them. The model sees each tool
as `sentry__<tool>`, for example `sentry__search_issues`.

## Tools

| Tool | Use |
|---|---|
| `find_organizations`, `find_projects` | The organization and project slugs the other tools need. |
| `search_issues` | Grouped issues, with their first and last seen times, events and users. Not counts. |
| `search_errors` | Error events, with counts, trends and top error types. |
| `search_logs` | Log lines, with counts and trends. |
| `search_traces` | Spans and traces, for latency and throughput questions. |
| `search_metrics` | Metric values, percentiles and totals. |
| `get_sentry_resource` | One issue, event, trace, replay or dashboard, by Sentry URL or by type and ID. |

## Benchmark snapshot

`bench/tools/sentry.json` lists the tools above, so benchmark scenarios can
give the model the same tools. It is copied from `toolDefinitions.json`,
the file of tool definitions that the server's source keeps in
`packages/mcp-core/src`, in
[getsentry/toolkit](https://github.com/getsentry/toolkit) at commit
`8860f39`, with the descriptions and input schemas unchanged but for one dash in a
description, which is a semicolon here. The public
scenario
[`sentry-regression-after-release`](../../bench/scenarios/public/sentry-regression-after-release.yaml)
uses it.
