# The memory source

The memory source (`type: memory`) lets an investigation search Sidekick's
own memory: the past investigations whose verdict a responder confirmed,
and the notes people wrote about services. It reads Sidekick's database and
only reads. This page lists its keys and its tools.

Every prompt already shows up to 3 similar confirmed investigations, as
[`memory`](../configuration.md#memory) describes; the source adds tools
the model can use to look further on its own.

## Config

```yaml
sources:
  memory:
    type: memory
```

The source has only the keys every source has (`tools.allow`, `tools.deny`,
`tools.rename`, `timeout` and `concurrency`); see
[`sources`](../configuration.md#sources). Add `memory` to a route's
`sources` list, when the route has one, to offer its tools there.

## Tools

The model sees each tool as `<source>__<tool>`, for example
`memory__search`.

| Tool | Returns |
|---|---|
| `search` | Up to 10 confirmed investigations whose alert name, summary or cause match a query, best match first: each one's confirmed verdict and cause, service, alert name, summary and when it was confirmed. `service` and `verdict` narrow the search. |
| `service_notes` | The notes about one service, and when they last changed. |

How it behaves:

- **Only confirmed investigations.** An investigation enters memory when a
  responder marks its verdict correct, and leaves it when a later Wrong
  takes the confirmation back. An entry an admin disabled never matches.
- **Queries.** `search` takes words as a web search does: every word must
  match, words in quotes match as a phrase, `or` between words matches
  either, and a word with a leading `-` must not appear. Words match in
  their English base form, so `deploys` finds `deploy`.
- **No data.** A search that matches nothing, and a service without notes,
  are reported as no data.
- **Evidence.** Each investigation found is listed as a past incident in
  the evidence of the call.
- **Size.** Summaries and causes are cut to 1,000 bytes and notes to 8,000
  bytes; a result that was cut is marked as cut.
