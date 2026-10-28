# The SigNoz source

SigNoz needs no source type of its own: Sidekick reaches it through the
SigNoz MCP server, with a source of `type: mcp`. This lets an investigation
read the logs, traces and metrics of your services, and the alerts and
services SigNoz knows, with read-only tools. This page shows how to reach
the server, the access the API key needs, and the config to use. The keys
every MCP source takes are in [Configuration](../configuration.md#mcp-sources-type-mcp),
and how an MCP source proves who it is is in
[Authenticating MCP sources](mcp-authentication.md).

## Reaching the server

Pick one.

- **SigNoz Cloud.** Use the hosted server for your region,
  `https://mcp.<region>.signoz.cloud/mcp`, where `<region>` is `us`, `eu`
  or `in`. It needs your instance URL as well as the key, in the
  `X-SigNoz-URL` header.
- **Self-hosted SigNoz.** Run the
  [`signoz/signoz-mcp-server`](https://hub.docker.com/r/signoz/signoz-mcp-server)
  image in HTTP mode next to Sidekick, for example as a second container
  of the Sidekick pod or as its own Deployment. Give it the address of
  SigNoz and the key, and point the source at it:

  ```sh
  docker run -p 8000:8000 \
    -e TRANSPORT_MODE=http -e MCP_SERVER_PORT=8000 \
    -e SIGNOZ_URL=https://signoz.internal.example.com \
    -e SIGNOZ_API_KEY="$SIGNOZ_API_KEY" \
    signoz/signoz-mcp-server:<version>
  ```

  Pin a version tag. The server listens on every interface unless you set
  `MCP_SERVER_HOST=127.0.0.1`, and a server that holds a key serves every
  caller with it, so keep it reachable only from Sidekick (a
  `NetworkPolicy`, or loopback in a sidecar).

## Access

Create a service account for Sidekick in SigNoz (**Settings**, **Service
Accounts**; only admins can), give it the managed `signoz-viewer` role,
which is read-only, and generate an API key on its **Keys** tab. Keep the
key in a Secret:

```sh
kubectl create secret generic signoz-mcp --from-literal=apiKey=<key>
```

A tool that needs more than the role allows fails with `PERMISSION_DENIED`,
which the investigation reports as a failed call. Use a service account of
its own for Sidekick, so the key can be rotated without touching anything
else.

## Config

```yaml
sources:
  signoz:
    type: mcp
    url: https://mcp.us.signoz.cloud/mcp
    headers:
      SIGNOZ-API-KEY: { secret: { name: signoz-mcp, key: apiKey } }
      X-SigNoz-URL: { value: https://example.signoz.cloud }
    tools:
      allow:
        - signoz_list_alerts
        - signoz_list_alert_rules
        - signoz_get_alert_history
        - signoz_list_services
        - signoz_get_service_top_operations
        - signoz_list_metrics
        - signoz_query_metrics
        - signoz_get_field_keys
        - signoz_get_field_values
        - signoz_search_logs
        - signoz_aggregate_logs
        - signoz_search_traces
        - signoz_aggregate_traces
        - signoz_get_trace_details
```

For a self-hosted server that holds the key itself, use its address, for
example `http://127.0.0.1:8000/mcp`, and no `headers`.

The server also offers tools that create, replace and delete alerts,
dashboards, saved views and notification channels. SigNoz marks each of
them as writing, and Sidekick leaves out a tool that is marked so, but
`tools.allow` is the safer guard: it names exactly the tools that read, so
a tool added by a later server version stays hidden until you add it. The
model sees each tool as `signoz__<tool>`, for example
`signoz__signoz_search_logs`.

## Tools

| Tool | Use |
|---|---|
| `signoz_list_alerts` | The alerts that are firing, silenced or inhibited now, with their labels. |
| `signoz_list_alert_rules` | The configured alert rules, also those that are not firing. |
| `signoz_get_alert_history` | When one rule fired and recovered, to see whether it flaps. |
| `signoz_list_services` | The services that have traces in a time window, with call and latency summaries. |
| `signoz_get_service_top_operations` | The operations of one service ranked by p99, with calls and errors. |
| `signoz_list_metrics`, `signoz_query_metrics` | Find a metric, and read its values, trends or a formula over metrics. |
| `signoz_get_field_keys`, `signoz_get_field_values` | The field names and values to filter logs, traces and metrics by. |
| `signoz_search_logs`, `signoz_aggregate_logs` | Log lines that match a filter, and counts and breakdowns of them. |
| `signoz_search_traces`, `signoz_aggregate_traces` | Spans or traces that match a filter, and statistics over spans. |
| `signoz_get_trace_details` | The spans of one trace. |

The server adds a `searchContext` argument to every tool, for its own
telemetry. The benchmark snapshot leaves it out.

## Benchmark snapshot

`bench/tools/signoz.json` lists the tools above, so benchmark scenarios can
give the model the same tools. The names and descriptions are those of the
server's `manifest.json` and the input schemas are those of the tool
registrations in `internal/handler/tools`, both at version 0.16.0 of
[SigNoz/signoz-mcp-server](https://github.com/SigNoz/signoz-mcp-server)
(commit `4861473`); long parameter descriptions are shortened. The public
scenario
[`error-rate-signoz-bad-secret-reference`](../../bench/scenarios/public/error-rate-signoz-bad-secret-reference.yaml)
uses it.
