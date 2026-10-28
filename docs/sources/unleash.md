# The Unleash source

The Unleash source (`type: unleash`) lets an investigation see whether
someone changed a feature flag around the time a problem started: who turned
a flag on or off or changed its strategies, and what state the flag is in
now. It calls the Unleash Admin API with one service account token, and it
only reads. This page lists its keys, its tools and the access the token
needs.

## Config

```yaml
sources:
  flags:
    type: unleash
    baseURL: https://unleash.example.com
    tokenSecret: { name: sidekick-unleash, key: token }
    projects:
      - key: default
        environments: [production, development]
```

| Key | Default | Meaning |
|---|---|---|
| `baseURL` | none | The root of your Unleash instance, such as `https://unleash.example.com`. It may hold a path if Unleash is served under one. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses, because the token is sent with each request. Required. |
| `tokenSecret` | none | `{ name, key }` of the Secret that holds the service account token. Required. |
| `projects` | none | The projects the tools may read, each with its `key` (the project ID; Unleash's default project is `default`) and the `environments` (names) in it they may read. Required, at least one project with at least one environment. A call for any other project or environment is a tool error, and nothing is sent to Unleash. |

Keys are the IDs and names Unleash shows, not display names. They may hold
letters, digits, `.`, `_` and `-`, and must start with a letter or a digit,
so no key can change a request path or add to a filter.

The shared source keys (`tools.allow`, `tools.deny`, `tools.rename`,
`timeout` and `concurrency`) work as for every source; see
[`sources`](../configuration.md#sources). `timeout` may be at most `60s`.

## Tools

The model sees each tool as `<source>__<tool>`, for example
`flags__list_flag_changes`.

| Tool | Returns |
|---|---|
| `list_flag_changes` | The events for the flags of one project and environment in a time range, newest first, or for one flag: when, which flag, what was done (the Unleash event type, such as `feature-environment-enabled` or `feature-strategy-update`), who did it, Unleash's summary of the change, and a link to the flag. `since` is required, and the range may be at most 7 days long. |
| `get_flag` | One flag's current state in one environment: whether it is on, its type (such as `release` or `kill-switch`), how many strategies and dependencies it has, and when it last changed there. |

Both take `project` and `environment`. `project` may be left out when
`projects` lists one, and `environment` when the project lists one.

How it behaves:

- **Start-up never waits for Unleash.** The first tool call is the first
  request. The token is read from its Secret for each request, so a rotated
  token needs no restart, and it is never logged. It is sent as it is in
  the `Authorization` header, as Unleash expects.
- **Events by day.** Unleash filters events by whole days, so a call asks
  for the days around the time range and Sidekick drops the events outside
  it. Times in results are UTC.
- **Events without an environment.** The call filters by environment, so
  events that belong to no environment, such as creating, archiving or
  tagging a flag, are not listed. Turning a flag on or off and changing its
  strategies are listed.
- **Who and why.** `actor` is the user, or the API token or integration
  name, that Unleash records for the event. If your instance anonymises its
  event log, the actor is anonymised too. Unleash events carry no comment,
  so `comment` is always empty.
- **Last change.** The flag itself has no time of its last change, so
  `get_flag` reads the newest event of the flag in the environment. If
  there is none, or the token may not read events, `lastChanged` is left
  out and the rest of the state is still returned.
- **Archived flags.** Unleash does not give an archived flag through this
  API, so `get_flag` for one returns no data.
- **Empty results.** A range without changes, and a project or flag Unleash
  does not have or the token may not read, return no data, not an error,
  with a link to the project's page when there is no flag to link.
- **Errors the model can act on.** A project or environment outside
  `projects`, a key that is not one, a missing `since`, a range longer than
  7 days, or a request Unleash refuses as invalid (a 400, for example) is a
  tool error.
- **Failures.** A refused token (401), a token that may not read (403),
  rate limits (429), server errors, an Unleash that cannot be reached and
  an empty token Secret fail the call. After 5 failures in a row the source
  is skipped for 60 seconds, as for every source.
- **Evidence.** Results link to the flags' pages in the Unleash UI, under
  `baseURL`, as `<baseURL>/projects/<project>/features/<flag>`. Links are
  built from the config and the keys, never taken from an answer: a
  Markdown link in an event's summary that points to another host is
  reduced to its text.

## Limits

- `list_flag_changes` returns up to `limit` changes (default 20, at most
  100). A call asks Unleash for up to 100 events at a time and makes at
  most 10 requests. Summaries are cut at 2 KiB, and names, actions and who
  made the change at 256 bytes. The changes of one result are at most
  128 KiB in all, and a result that was cut, or that has more changes than
  `limit`, is marked as cut.
- `get_flag` returns counts of strategies and dependencies, not the
  strategies or their constraints themselves.
- Summaries in the event log hold the names of flags and users, written by
  people and integrations, so they are passed to the model as untrusted
  text, like every source result.

## Service account token

Create a service account for Sidekick in the Unleash UI under **Admin
settings**, **Service accounts**:

- **Role.** Give it the **Viewer** root role. Sidekick only sends `GET`
  requests to the event search (`/api/admin/search/events`) and feature
  flag (`/api/admin/projects/<project>/features/<flag>`) endpoints, so read
  access is all it needs.
- **Token.** Create a token for the service account, when you create it or
  later. Unleash shows the token's value only once, so copy it then.
  Choose an expiry date you can keep up with, because an expired token
  fails every call with a 401.

Put the token in the Secret that `tokenSecret` names:

```sh
kubectl create secret generic sidekick-unleash --from-literal=token='*:*.0123456789abcdef...'
```

Unleash's documentation recommends service accounts for integrations. If
your plan has none, a personal access token of a dedicated user with the
Viewer role works the same way, but it stops working when that user is
removed.

Do not use an Admin API token of type `ADMIN` or a client token: the first
can change everything, and the second cannot call the Admin API.
