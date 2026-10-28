# The Elasticsearch and OpenSearch source

The Elasticsearch source (`type: elasticsearch`) lets investigations search
the logs that Elasticsearch or OpenSearch keeps, such as an ELK or EFK
stack: find the log lines around an alert, count errors over time to see
when they started, and list the fields an index has. `flavor` says which of
the two the cluster is. Every tool only reads, and only the index patterns
you list can be read.

This page lists the source's keys and tools, the read-only role it needs,
and how to set up links to Kibana or OpenSearch Dashboards. The keys are
also in [Configuration](../configuration.md#elasticsearch-and-opensearch-sources-type-elasticsearch).

## Config

```yaml
sources:
  logs:
    type: elasticsearch
    flavor: elasticsearch
    baseURL: https://logs.example.com:9200
    apiKeySecret: { name: sidekick-elasticsearch, key: apiKey }
    indexPatterns: [logs-*, filebeat-*]
    discoverURL: "https://kibana.example.com/app/discover#/?_g=(time:(from:'{{.From}}',to:'{{.To}}'))&_a=(query:(language:lucene,query:'{{.Query}}'))"
```

For OpenSearch, sign in with a user and its password:

```yaml
sources:
  logs:
    type: elasticsearch
    flavor: opensearch
    baseURL: https://opensearch.example.com:9200
    basicAuth:
      username: sidekick
      passwordSecret: { name: sidekick-opensearch, key: password }
    indexPatterns: [logs-*]
```

| Key | Default | Meaning |
|---|---|---|
| `flavor` | `elasticsearch` | `elasticsearch` or `opensearch`. |
| `baseURL` | none | The root of the cluster's REST API. Required. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses, because the credentials are sent with each request. |
| `apiKeySecret` | none | `{ name, key }` of the Secret that holds an Elasticsearch API key in its encoded form, sent as `Authorization: ApiKey <key>`. Only with `flavor: elasticsearch`. |
| `basicAuth.username` | none | The user Sidekick signs in as. |
| `basicAuth.passwordSecret` | none | `{ name, key }` of the Secret that holds that user's password. |
| `indexPatterns` | none | The index patterns the tools may read, such as `logs-*`. Required. Each one uses lower-case letters, digits, `.`, `_`, `-` and `*`, and must name more than every index, so `*` alone is refused. |
| `timeField` | `@timestamp` | The field that holds each log's time. Searches are bounded and sorted on it. |
| `messageField` | `message` | The field that holds the log message. |
| `defaultFields` | see below | The fields `search_logs` returns when the call names none, next to the time and message fields. At most 50. |
| `discoverURL` | none | A Go template for evidence links to the same search in Kibana Discover or OpenSearch Dashboards; see [Links](#links). Without it, evidence names the search but has no link. |

Set exactly one of `apiKeySecret` and `basicAuth`. Without `defaultFields`,
`search_logs` returns the time and message fields and `log.level`,
`service.name`, `host.name`, `error.message` and `trace.id`, the Elastic
Common Schema names; a field a log lacks is left out.

The shared source keys (`tools.allow`, `tools.deny`, `tools.rename`,
`timeout` and `concurrency`) work as for every source; see
[`sources`](../configuration.md#sources). `timeout` may be at most `60s`.

Amazon OpenSearch Service domains that need AWS Signature Version 4 are not
supported yet; a domain with fine-grained access control and an internal
user works with `basicAuth`.

## Tools

The model sees each tool as `<source>__<tool>`, for example
`logs__search_logs`.

| Tool | Returns |
|---|---|
| `search_logs` | The logs that match a Lucene query string in a time range, newest first: each hit's index, ID and chosen fields, and how many logs matched in all. |
| `count_logs` | How many logs match a query string in each time bucket of a range, and, with `splitBy`, the same for each of the 10 most common values of one field, such as `service.name`. |
| `list_fields` | The fields of an index pattern with their types, and whether each can be searched and counted by. |

How it behaves:

- **Start-up never waits for the cluster.** The first tool call is the
  first request. The API key or password is read from its Secret for each
  request, so a rotated one needs no restart.
- **Index patterns.** Every tool takes `index`, which may be left out when
  `indexPatterns` lists one pattern. It must be one index or pattern that a
  listed pattern covers: with `logs-*` listed, `logs-*`, `logs-checkout-*`
  and `logs-2026.05.04` are allowed, and `log*`, `logs-a,secrets`,
  `-logs-a`, `_all` and `remote:logs-a` are not. Anything else is a tool
  error, and nothing is sent.
- **Queries.** The query string goes to the cluster as the model wrote it,
  as a `query_string` query inside the call's time range. The search only
  reads the call's index, so the query cannot reach other indexes; a
  clause on the `_index` field must still name one index or pattern the
  list covers, or the call is refused. Sidekick builds every request body
  itself; a call never sends one.
- **Time range.** `search_logs` and `count_logs` need `since`; `until`
  defaults to now. The range may be at most 7 days.
- **No data.** A search that matches nothing, an index pattern that matches
  no index, and an index that does not exist are reported as no data.
- **Errors the model can act on.** A query string the cluster cannot parse,
  a field it cannot count by, or a privilege the role lacks goes to the
  model as a tool error with the cluster's message.
- **Failures.** Refused credentials, rate limits, server errors and a
  cluster that cannot be reached fail the call. After 5 failures in a row
  the source is skipped for 60 seconds, as for every source.
- **Evidence.** `search_logs` and `count_logs` link to the same search in
  Kibana or OpenSearch Dashboards when `discoverURL` is set.

## Limits

- **Hits.** `search_logs` returns 50 hits by default and at most 200, and
  at most 50 fields of each. A result that holds fewer hits than matched is
  marked as cut, and says how many matched.
- **Buckets.** `count_logs` makes about 60 buckets by default, and at most
  200; an `interval` that gives more is a tool error that says the
  shortest one that fits.
- **Size.** Each field value of a hit is cut at 2 KiB, and names at 512
  bytes. A result stops adding hits or fields at 256 KiB. A result that was
  cut says so, and so does a search where some shards failed or the search
  timed out.

## Permissions

Give Sidekick a role that can read the listed index patterns and nothing
else. `read` lets it search, count and list fields; `view_index_metadata`
lets it read the indexes' mappings. Neither can change anything.

### Elasticsearch

Create the role, with the same patterns as `indexPatterns`:

```sh
curl -u elastic -X POST "https://logs.example.com:9200/_security/role/sidekick_reader" \
  -H 'Content-Type: application/json' -d '{
  "indices": [
    { "names": ["logs-*", "filebeat-*"], "privileges": ["read", "view_index_metadata"] }
  ]
}'
```

Then create an API key limited to the same privileges, and put the
`encoded` value of the answer in the Secret that `apiKeySecret` names:

```sh
curl -u elastic -X POST "https://logs.example.com:9200/_security/api_key" \
  -H 'Content-Type: application/json' -d '{
  "name": "sidekick",
  "role_descriptors": {
    "sidekick_reader": {
      "indices": [
        { "names": ["logs-*", "filebeat-*"], "privileges": ["read", "view_index_metadata"] }
      ]
    }
  }
}'

kubectl create secret generic sidekick-elasticsearch --from-literal=apiKey=<encoded>
```

An API key can never do more than the user who created it, so create it
as a user that can read those indexes. On Elastic Cloud, you can create the
same key in Kibana under **Stack Management > API keys**. To sign in with
a user instead, give a user the `sidekick_reader` role and use `basicAuth`.

### OpenSearch

With the security plugin, create a role that has the `read` action group
on the listed patterns, and map a user to it:

```sh
curl -u admin -X PUT "https://opensearch.example.com:9200/_plugins/_security/api/roles/sidekick_reader" \
  -H 'Content-Type: application/json' -d '{
  "index_permissions": [
    { "index_patterns": ["logs-*"], "allowed_actions": ["read"] }
  ]
}'

curl -u admin -X PUT "https://opensearch.example.com:9200/_plugins/_security/api/internalusers/sidekick" \
  -H 'Content-Type: application/json' -d '{ "password": "<password>", "backend_roles": [] }'

curl -u admin -X PUT "https://opensearch.example.com:9200/_plugins/_security/api/rolesmapping/sidekick_reader" \
  -H 'Content-Type: application/json' -d '{ "users": ["sidekick"] }'

kubectl create secret generic sidekick-opensearch --from-literal=password='<password>'
```

The `read` action group covers searches, counts and field lists, and
nothing that writes.

### Aliases

The patterns match index, alias and data stream names alike. Sidekick
checks names, not where an alias points, so an alias whose name matches a
pattern but that points at other indexes would let it search those
indexes too, as far as the role allows. Do not create such an alias.

## Links

`discoverURL` is a Go template with four fields, each already escaped for
a URL and for a quoted string in the `_g`, `_a` and `_q` state parameters:

| Field | Holds |
|---|---|
| `.Index` | The index pattern of the call, such as `logs-*`. |
| `.Query` | The query string of the call, or nothing. |
| `.From` | The start of the time range, such as `2026-05-04T09:00:00.000Z`. |
| `.To` | The end of the time range. |

Put each field between single quotes in the template. `sidekick config
validate` checks that the template renders an absolute `http` or `https`
URL.

Kibana Discover opens a data view by its ID, not by its pattern. With one
data view for the listed patterns, put its ID in the template:

```yaml
discoverURL: "https://kibana.example.com/app/discover#/?_g=(time:(from:'{{.From}}',to:'{{.To}}'))&_a=(dataSource:(dataViewId:'<data view id>',type:dataView),query:(language:lucene,query:'{{.Query}}'))"
```

With several, pick the ID by pattern with the template's `if`:

```yaml
discoverURL: "https://kibana.example.com/app/discover#/?_g=(time:(from:'{{.From}}',to:'{{.To}}'))&_a=(dataSource:(dataViewId:'{{if eq .Index \"audit-%2A\"}}<audit view id>{{else}}<logs view id>{{end}}',type:dataView),query:(language:lucene,query:'{{.Query}}'))"
```

`.Index` is escaped, so compare it with the escaped form, where `*` is
`%2A`.

OpenSearch Dashboards 2.10 and later open Discover through the data
explorer, by index pattern ID:

```yaml
discoverURL: "https://dashboards.example.com/app/data-explorer/discover#?_a=(discover:(columns:!(_source)),metadata:(indexPattern:'<index pattern id>',view:discover))&_g=(time:(from:'{{.From}}',to:'{{.To}}'))&_q=(query:(language:lucene,query:'{{.Query}}'))"
```

The ID is in the address bar when you open the data view or index pattern
in Kibana or OpenSearch Dashboards.
