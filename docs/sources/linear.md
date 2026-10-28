# The Linear source

Linear needs no source type of its own: Sidekick reaches it through
Linear's hosted MCP server, with a source of `type: mcp`. This lets an
investigation look up the issues, comments and projects of your workspace,
for example to see that an alert is already tracked by a ticket or an
incident, with read-only tools. This page shows the endpoint, the access
the key needs, and the config to use. The keys every MCP source takes are
in [Configuration](../configuration.md#mcp-sources-type-mcp), and how an
MCP source proves who it is is in
[Authenticating MCP sources](mcp-authentication.md).

## Reaching the server

Linear hosts the server, so there is nothing to run. Use the read-only
endpoint, `https://mcp.linear.app/mcp/readonly`, which only ever lists
tools that read. The default endpoint, `https://mcp.linear.app/mcp`, also
lists the tools that create and change issues. The `/sse` endpoint is
deprecated; Sidekick uses streamable HTTP.

## Access

Create a Linear API key for Sidekick (**Settings**, **Account**,
**Security & access**) with only the **Read** permission, ideally under a
user of its own that can see just the teams Sidekick should look at. Keep
it in a Secret:

```sh
kubectl create secret generic linear-mcp --from-literal=apiKey=<key>
```

The server accepts the key as a bearer token. It does not take OAuth from
Sidekick; see [Authenticating MCP sources](mcp-authentication.md).

## Config

```yaml
sources:
  linear:
    type: mcp
    url: https://mcp.linear.app/mcp/readonly
    headers:
      Authorization: { secret: { name: linear-mcp, key: apiKey }, prefix: "Bearer " }
    tools:
      allow:
        - list_issues
        - get_issue
        - list_comments
        - list_projects
        - get_project
        - list_issue_statuses
```

The read-only endpoint and the **Read** key already keep every write out;
`tools.allow` limits what the model sees to what an investigation needs, so
the tool list stays short. Add other read tools, such as `list_teams`,
`list_cycles` or `get_document`, if you want them. The model sees each tool
as `linear__<tool>`, for example `linear__list_issues`.

## Tools

| Tool | Use |
|---|---|
| `list_issues` | Issues filtered by text, team, state, label, assignee, project or update time. Use it to find a ticket or incident that covers an alert. |
| `get_issue` | One issue with its description, status, priority and attachments. |
| `list_comments` | The comments on one issue, for the workaround or the latest news. |
| `list_projects`, `get_project` | The projects of the workspace, and one project's details. |
| `list_issue_statuses` | The statuses of a team, to tell open from closed. |

## Benchmark snapshot

`bench/tools/linear.json` lists the tools above, so benchmark scenarios can
give the model the same tools. Linear does not publish its tool list, so
the names, descriptions and input schemas are copied from the generated
wrappers of the server's tool list in
[vercel-labs/mcp-to-ai-sdk](https://github.com/vercel-labs/mcp-to-ai-sdk/tree/main/mcps/mcp.linear.app/sse)
(commit `e4cc297`, September 2025). The server has changed since: it takes
more arguments on some tools, and its newer tools, such as `save_issue`,
replace others. Refresh the snapshot from a `tools/list` of your own
workspace when you need the current schemas. The public scenarios
[`known-issue-linear-export-timeout`](../../bench/scenarios/public/known-issue-linear-export-timeout.yaml)
and
[`triage-duplicate-second-monitor`](../../bench/scenarios/public/triage-duplicate-second-monitor.yaml)
use it.
