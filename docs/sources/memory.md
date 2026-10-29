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
  the evidence of the call. With `server.publicURL` set, each one links
  to the investigation's page in the web UI, and so do the examples in
  the prompt.
- **Size.** Summaries and causes are cut to 1,000 bytes and notes to 8,000
  bytes; a result that was cut is marked as cut.

## Search by meaning

With [vector memory](../configuration.md#vector-memory-memoryvector) on,
`search` and the examples in each prompt also find entries that mean the
same as the query or the alert in other words. Sidekick embeds each entry,
and the query or the alert's text, with the embeddings model of
`memory.vector.embeddings`, and takes the entries nearest by cosine
distance. It merges them with the full-text matches by reciprocal rank
fusion: an entry scores the sum of 1/(60 + rank) over the lists it is in,
and the best scores come first. An entry that is in both lists beats one
that is in one. The examples that share the alert's fingerprint, or its
service and alert name, still come first and are not changed.

How it behaves:

- **Off by default, and optional.** With `memory.vector.enabled` false,
  nothing uses pgvector: no extension, no table and no vector query. Only
  a Postgres with the [pgvector](https://github.com/pgvector/pgvector)
  extension runs the vector migrations.
- **Embedding happens outside the confirmation.** Confirming a verdict, or
  changing the confirmation, queues a job that embeds the entry, so a slow
  embeddings call never holds up the click. An entry that changes is
  embedded again, a withdrawn one loses its vector at once, and the
  retention job's delete removes vectors with their entries. At start-up,
  Sidekick queues jobs for entries that have no vector, one from another
  model, or one of an older text of the entry, such as an entry that
  changed while vector memory was off. Switching to a model that returns
  vectors of the same length needs no manual step. A search compares
  vectors of the current model only, so until the backfill has embedded an
  entry again with the new model, that entry is found by full text alone.
- **Fall back to full text.** When the embeddings call fails, Sidekick
  logs a warning, without the text, and answers with full-text matches
  alone. An investigation never fails because of vectors.
- **Cost.** Each entry is embedded once, when it is confirmed or changed,
  and each search or investigation embeds up to 2,000 bytes of text, so the
  cost is small. The calls are counted in the model metrics with
  `role="embed"`, and priced when the model has a price (see
  [`memory.vector`](../configuration.md#vector-memory-memoryvector)).
- **Entries are cut.** An entry's text, its alert name, service, summary
  and cause, is cut to 8,000 bytes before it is embedded.

### Changing the length of the vectors

The length of the vectors is set when the embedding column is made, from
`memory.vector.embeddings.dimensions`. If you change `dimensions`, for
example by moving to a model that returns longer vectors, Sidekick refuses
to start, and `sidekick migrate up` fails, with this message:

```
memory_embeddings holds 1024-dimension vectors but memory.vector.embeddings.dimensions is 1536; run `sidekick migrate vector-reset` to empty the table and embed the memory entries again
```

Vectors are derived data, so the fix is to empty the table and let Sidekick
embed every entry again:

```sh
sidekick migrate vector-reset --config /etc/sidekick/sidekick.yaml
```

The command asks for confirmation; answer `yes`, or pass `--yes` to skip
the question. It empties `memory_embeddings` and takes the length off its
column, in one transaction, and writes one audit entry, with the actor
`system`, the action `memory.vectors_reset`, and the old and the new
length. The next start, or `sidekick migrate up`, gives the column the new
length, and the start-up backfill embeds every entry again, which costs one
embeddings call for each entry. Until it has, searches use full text for the
entries that have no vector yet.
