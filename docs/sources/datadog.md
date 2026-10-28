# The Datadog source

Datadog needs no source type of its own: Sidekick reaches it through
Datadog's managed MCP server, with a source of `type: mcp`. This lets an
investigation read your logs, metrics, spans, traces, events, monitors and
incidents with read-only tools. This page shows the endpoint, the access
the token needs, and the config to use. The keys every MCP source takes are
in [Configuration](../configuration.md#mcp-sources-type-mcp), and how an
MCP source proves who it is is in
[Authenticating MCP sources](mcp-authentication.md).

## Reaching the server

Datadog hosts the server, so there is nothing to run. Use the endpoint of
your [Datadog site](https://docs.datadoghq.com/getting_started/site/):

| Site | Endpoint |
|---|---|
| US1 | `https://mcp.datadoghq.com/v1/mcp` |
| US3 | `https://mcp.us3.datadoghq.com/v1/mcp` |
| US5 | `https://mcp.us5.datadoghq.com/v1/mcp` |
| EU | `https://mcp.datadoghq.eu/v1/mcp` |
| AP1 | `https://mcp.ap1.datadoghq.com/v1/mcp` |
| AP2 | `https://mcp.ap2.datadoghq.com/v1/mcp` |
| UK1 | `https://mcp.uk1.datadoghq.com/v1/mcp` |

The US2-FED and GOV sites are not supported. The server offers its tools
in toolsets, and without a `toolsets` query parameter it serves only the
`core` toolset, which has all the tools below. Datadog may change these
endpoints and tools; the page of
[its MCP server](https://docs.datadoghq.com/bits_ai/mcp_server/) is the
reference.

An organization admin must turn on **MCP Access** in **Organization
Settings**. Leave **MCP Write Access** off: Datadog then hides every tool
that creates or changes something.

## Access

Create a service account for Sidekick, give it a role that has the
`mcp_read` permission and the read permission of each product Sidekick
should look at (Logs Read Data, Logs Read Index Data, APM Read, Metrics,
Monitors Read, Incidents Read, Events, and so on), and no write
permission. Then create a service access token for the account and keep it
in a Secret:

```sh
kubectl create secret generic datadog-mcp --from-literal=token=<token>
```

The server uses the permissions of the account, so Sidekick cannot see
what the account cannot. Use a service access token rather than a personal
one. An API key and an application key of the account, sent in the
`DD_API_KEY` and `DD_APPLICATION_KEY` headers, work as well.

## Config

```yaml
sources:
  datadog:
    type: mcp
    url: https://mcp.datadoghq.com/v1/mcp
    headers:
      Authorization: { secret: { name: datadog-mcp, key: token }, prefix: "Bearer " }
    tools:
      allow:
        - search_datadog_logs
        - analyze_datadog_logs
        - get_datadog_metric
        - get_datadog_metric_context
        - search_datadog_metrics
        - search_datadog_monitors
        - search_datadog_events
        - search_datadog_incidents
        - get_datadog_incident
        - search_datadog_spans
        - aggregate_spans
        - get_datadog_trace
        - search_datadog_entities
```

The `core` toolset also has tools that create, edit and delete notebooks.
Datadog's documentation does not say whether each tool carries a read-only
marking, and Sidekick keeps a tool that has none, so `tools.allow` is what
keeps the writing ones hidden. The model sees each tool as `datadog__<tool>`, for example
`datadog__search_datadog_logs`. Run `sidekick doctor` to see the tools
Sidekick kept.

## Tools

| Tool | Use |
|---|---|
| `search_datadog_logs` | Log lines that match a query in a time range. |
| `analyze_datadog_logs` | Counts and aggregates over logs, written as SQL. |
| `get_datadog_metric`, `get_datadog_metric_context`, `search_datadog_metrics` | Metric values over time, a metric's tags and metadata, and metric names. |
| `search_datadog_monitors` | Monitors with their status, thresholds and conditions. |
| `search_datadog_events` | Events such as monitor alerts, deploys and infrastructure changes. |
| `search_datadog_incidents`, `get_datadog_incident` | Incidents and the details of one, to see whether an alert is already covered. |
| `search_datadog_spans`, `aggregate_spans`, `get_datadog_trace` | Spans, statistics over spans such as p95 by resource, and one full trace. |
| `search_datadog_entities` | A service's owner and the services it calls and is called by. |

Most tools take a `max_tokens` argument that cuts a long answer.

## Benchmark snapshot

`bench/tools/datadog.json` lists the tools above, so benchmark scenarios
can give the model the same tools. Datadog does not publish the input
schemas of its tools, so the snapshot is rebuilt from what it does publish:
the names and descriptions come from the
[tools reference](https://docs.datadoghq.com/mcp_server/tools/) (read on
2026-10-28), and the arguments from the examples of the
[Datadog Observability power](https://github.com/kirodotdev/powers/tree/main/datadog),
which Datadog authored, and from the argument names that Datadog's own
documentation and open-source repositories use (`query`, `from`, `to`,
`storage_tier`, `include_tag_values`, `group_by.interval`). Argument names
that no source shows, such as `metric_name` and `compute`, are a best
guess. The snapshot decides only what a benchmark model is allowed to send
to a mock; update it from a `tools/list` of your own account when Datadog
changes its tools. The public scenario
[`error-rate-datadog-duplicate-of-incident`](../../bench/scenarios/public/error-rate-datadog-duplicate-of-incident.yaml)
uses it.
