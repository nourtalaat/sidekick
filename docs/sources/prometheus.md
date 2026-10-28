# Prometheus source

The Prometheus source (`type: prometheus`) lets investigations read the
metrics, alerts and alert rules of Prometheus and the servers that share
its HTTP API: Thanos, Mimir, Cortex, Grafana Cloud Metrics and
VictoriaMetrics. It can also read an Alertmanager, for the alerts it holds
and the silences that mute them. It only reads.

This page lists the source's keys and tools, the access it needs, and its
limits. The shared source keys are in
[Configuration](../configuration.md#prometheus-sources-type-prometheus).

There is no official MCP server for Prometheus, and the community ones
cannot keep range queries small, so a wide query fills the 256 KiB cut of
MCP results. This source picks the step of a range query itself and sums
up each series, so a result stays small and still shows what happened.

## Config

A Prometheus in the same cluster, with no credentials:

```yaml
sources:
  prometheus:
    type: prometheus
    baseURL: http://prometheus-operated.monitoring:9090
    alertmanagerURL: http://alertmanager-operated.monitoring:9093
```

A Mimir tenant with basic auth, linking to Grafana Explore:

```yaml
sources:
  metrics:
    type: prometheus
    baseURL: https://mimir.example.com/prometheus
    alertmanagerURL: https://mimir.example.com/alertmanager
    basicAuth:
      username: sidekick
      passwordSecret: { name: sidekick-mimir, key: password }
    headers:
      X-Scope-OrgID: shop
    links:
      grafanaURL: https://grafana.example.com
      grafanaDatasource: mimir-shop
```

A server that asks for a client certificate, signed by a private CA:

```yaml
sources:
  prometheus:
    type: prometheus
    baseURL: https://prometheus.internal.example.com
    tls:
      certSecret: { name: sidekick-prometheus-tls, key: tls.crt }
      keySecret: { name: sidekick-prometheus-tls, key: tls.key }
      caSecret: { name: sidekick-prometheus-tls, key: ca.crt }
```

| Key | Default | Meaning |
|---|---|---|
| `baseURL` | none | The root of the Prometheus HTTP API, the part before `/api/v1`. Required. For Mimir it ends in `/prometheus`, for Grafana Cloud in `/api/prom`. |
| `alertmanagerURL` | none | The root of an Alertmanager API, the part before `/api/v2`. When set, `list_alerts` reads Alertmanager instead of Prometheus. The credentials and headers go to it too. |
| `bearerTokenSecret` | none | `{ name, key }` of the Secret that holds a token, sent as `Authorization: Bearer <token>`. |
| `basicAuth.username`, `basicAuth.passwordSecret` | none | A user name, and `{ name, key }` of the Secret that holds its password, for HTTP basic auth. Set either this or `bearerTokenSecret`. |
| `tls.certSecret`, `tls.keySecret` | none | `{ name, key }` of the Secrets that hold a PEM client certificate and its key, for mutual TLS. Set both or neither. |
| `tls.caSecret` | the system CAs | `{ name, key }` of the Secret that holds the PEM CA certificates to trust, for a server with a private CA. |
| `headers` | none | Extra headers sent with every request, such as `X-Scope-OrgID` for a Mimir or Cortex tenant. They may not hold credentials: `Authorization`, `Proxy-Authorization` and `Cookie` are refused, and credentials go in the keys above. |
| `links.prometheusURL` | `baseURL` | The root of the Prometheus web UI, for evidence links, when it is not at `baseURL`. |
| `links.grafanaURL`, `links.grafanaDatasource` | none | A Grafana and the UID of its data source for this server. When set, queries link to Grafana Explore instead of the Prometheus web UI. |

`baseURL` and `alertmanagerURL` must use `https` when a token, a password
or a client certificate is sent; plain `http` is then accepted only for
`localhost` and loopback addresses. Without credentials, plain `http` is
accepted, for a Prometheus in the same cluster. The secrets are read again
for each request, and the client certificate for each new connection, so a
rotated secret needs no restart.

The shared source keys (`tools.allow`, `tools.deny`, `tools.rename`,
`timeout` and `concurrency`) work as for every source; see
[`sources`](../configuration.md#sources). `timeout` may be at most `60s`.

## Tools

The model sees each tool as `<source>__<tool>`, for example
`prometheus__query_range`.

| Tool | Returns |
|---|---|
| `query` | An instant PromQL query at one time, by default now: each series' labels and value, or the value of a scalar. |
| `query_range` | A PromQL range query: each series' labels, the min, max, average and last of its values, and its points. |
| `list_alerts` | The alerts firing or pending in Prometheus, firing first. With `alertmanagerURL`, the Alertmanager alerts instead, with the silences and inhibitions that mute them. |
| `get_rules` | The alerting rules with one alert name: group, expression, `for`, labels, annotations, health, last error and last evaluation. |
| `list_series` | The labels of the series that match a series selector, such as `up{job="api"}`, in a time range. |
| `list_label_values` | The values of one label, such as `service` or `__name__`, optionally on the series that match a selector. |
| `list_targets` | The scrape targets that are down or unknown, with their last error. Healthy targets are counted, not listed. |

How it behaves:

- **Start-up never waits for Prometheus.** The first tool call is the
  first request.
- **Queries are checked first.** Every PromQL query and series selector is
  parsed with the Prometheus parser before it is sent. One that does not
  parse is a tool error with the parser's message, and nothing is sent.
  Because of this, MetricsQL extensions that are not PromQL are refused
  for VictoriaMetrics. `query_range` also refuses a query that returns a
  range vector, such as `up[5m]`, and says to wrap it in a function.
- **The step is picked for the model.** `query_range` takes no step: it
  splits the range into at most 250 points per series, so the last hour
  has a 15 second step and 7 days a step of about 40 minutes. Points are
  `[unix seconds, value]`, oldest first, and a value JSON cannot hold, such
  as `NaN` or `+Inf`, is kept as text and left out of the min, max,
  average and last.
- **No data.** A query, selector or label with no series or values, no
  alerts, no rule with the name, every target up, and an API the server
  does not have (Mimir has no targets API) are reported as no data, not
  as errors.
- **Errors the model can act on.** A query the server refuses or that
  fails while it runs, such as one that would load too many samples, a
  `403`, and an answer larger than 16 MiB go to the model as a tool error
  with the server's message, so it can narrow the query.
- **Failures.** A refused credential (`401`), rate limits, server errors
  and timeouts, a secret that cannot be read and a server that cannot be
  reached fail the call. After 5 failures in a row the source is skipped
  for 60 seconds, as for every source.
- **Evidence.** Query results link to the query in the Prometheus web UI
  (`/graph`), or in Grafana Explore when `links.grafanaURL` is set.
  `get_rules` links to its first rule's expression over the last hour,
  `list_alerts` to the alerts page of Prometheus, Grafana or Alertmanager,
  and `list_targets` to the targets page.

## Access

The Prometheus HTTP API has no permissions of its own: anyone who can
reach it can read every metric. The source sends only `GET` requests, and
only to these paths, so a proxy in front of the server can allow exactly
them and refuse the rest, such as the admin API under `/api/v1/admin`:

| Path | Used by |
|---|---|
| `/api/v1/query` | `query` |
| `/api/v1/query_range` | `query_range` |
| `/api/v1/alerts` | `list_alerts` without `alertmanagerURL` |
| `/api/v1/rules` | `get_rules` |
| `/api/v1/series` | `list_series` |
| `/api/v1/label/<name>/values` | `list_label_values` |
| `/api/v1/targets` | `list_targets` |
| `/api/v2/alerts`, `/api/v2/silences` under `alertmanagerURL` | `list_alerts` with `alertmanagerURL` |

For hosted and multi-tenant servers, give Sidekick a credential that can
only read:

- **Mimir and Cortex**: set the tenant in `headers` as `X-Scope-OrgID`. If
  a gateway checks credentials, give Sidekick its own user that reads only
  that tenant.
- **Grafana Cloud**: create an access policy with the `metrics:read`,
  `rules:read` and `alerts:read` scopes, and a token for it. Use the
  instance ID shown with the Prometheus endpoint as `basicAuth.username`
  and the token as the password.
- **OpenShift monitoring**: use the Thanos Querier route as `baseURL`
  and a service account token with the `cluster-monitoring-view` cluster
  role as `bearerTokenSecret`.

With `networkPolicy.restrictEgress`, allow the hosts and ports of
`baseURL` and `alertmanagerURL`.

## Limits

- **Time range.** `query_range`, `list_series` and `list_label_values`
  read at most 7 days, and the last hour by default.
- **Series and points.** `query` and `query_range` keep at most 50 series,
  and say how many there were; a series keeps at most 250 points, the
  newest when an instant query returns more. Use label matchers, `sum by`
  or `topk` to narrow a query with more series.
- **Lists.** `list_alerts` returns up to 50 alerts by default and at most
  200, `get_rules` at most 20 rules, `list_series` up to 100 series by
  default and at most 500, `list_label_values` up to 100 values by default
  and at most 1,000, and `list_targets` up to 50 targets by default and at
  most 200.
- **Size.** Annotations, warnings and error messages are cut at 2 KiB,
  rule expressions at 16 KiB, and names and label values at 512 bytes. A
  result stops adding items at 256 KiB, and an answer from the server may
  be at most 16 MiB. A result that was cut says so.
