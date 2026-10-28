# The LaunchDarkly source

The LaunchDarkly source (`type: launchdarkly`) lets an investigation see
whether someone changed a feature flag around the time a problem started:
who turned a flag on or off, changed its targeting or rolled it out, and
what state the flag is in now. It calls the LaunchDarkly REST API with one
access token, and it only reads. This page lists its keys, its tools and
the access the token needs.

## Config

```yaml
sources:
  flags:
    type: launchdarkly
    tokenSecret: { name: sidekick-launchdarkly, key: token }
    projects:
      - key: shop
        environments: [production, staging]
```

| Key | Default | Meaning |
|---|---|---|
| `tokenSecret` | none | `{ name, key }` of the Secret that holds the access token. Required. |
| `projects` | none | The projects the tools may read, each with its `key` and the `environments` (keys) in it they may read. Required, at least one project with at least one environment. A call for any other project or environment is a tool error, and nothing is sent to LaunchDarkly. |
| `baseURL` | `https://app.launchdarkly.com` | The root of your LaunchDarkly instance: `https://app.eu.launchdarkly.com` for the EU instance, `https://app.launchdarkly.us` for LaunchDarkly Federal. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses, because the token is sent with each request. |

Keys are LaunchDarkly keys, such as the project key in the project's
settings, not display names. They may hold letters, digits, `.`, `_` and
`-`, so no key can widen a request to other projects or flags.

The shared source keys (`tools.allow`, `tools.deny`, `tools.rename`,
`timeout` and `concurrency`) work as for every source; see
[`sources`](../configuration.md#sources). `timeout` may be at most `60s`.

## Tools

The model sees each tool as `<source>__<tool>`, for example
`flags__list_flag_changes`.

| Tool | Returns |
|---|---|
| `list_flag_changes` | The audit log entries for the flags of one project and environment in a time range, newest first, or for one flag: when, which flag, what was done (such as "turned on the flag"), who did it, the summary of the change, the comment left with it, and a link to the flag. `since` is required, and the range may be at most 7 days long. |
| `get_flag` | One flag's current state in one environment: whether it is on or archived, how many targeting rules, individual targets and prerequisites it has, what it serves when on and no rule matches (a variation or a percentage rollout), what it serves when off, its version, and when it last changed. |

Both take `project` and `environment`. `project` may be left out when
`projects` lists one, and `environment` when the project lists one.

How it behaves:

- **Start-up never waits for LaunchDarkly.** The first tool call is the
  first request. The token is read from its Secret for each request, so a
  rotated token needs no restart, and it is never logged.
- **API version.** Every request sends `LD-API-Version: 20240415`, so the
  answers keep their shape whatever API version the token was made with.
- **Empty results.** A range without changes, and a project or flag
  LaunchDarkly does not have or the token may not read, return no data,
  not an error, with a link to the environment's flags or to the flag.
- **Errors the model can act on.** A project or environment outside
  `projects`, a key that is not one, a missing `since`, a range longer
  than 7 days, or a request LaunchDarkly refuses (such as a 400 or a 403)
  is a tool error.
- **Failures.** A refused token (401), rate limits (429), server errors, a
  LaunchDarkly that cannot be reached and an empty token Secret fail the
  call. After 5 failures in a row the source is skipped for 60 seconds, as
  for every source.
- **Evidence.** Results link to the flags' pages in LaunchDarkly, under
  `baseURL`. A link in an audit log entry that points anywhere else is
  left out.

## Limits

- `list_flag_changes` returns up to `limit` changes (default 20, at most
  100). LaunchDarkly gives at most 20 audit log entries a page, so a call
  reads up to 8 pages. Summaries and comments are cut at 2 KiB, and names,
  actions and who made the change at 256 bytes. The changes of one result
  are at most 128 KiB in all, and a result that was cut, or that has more
  changes than `limit`, is marked as cut.
- `get_flag` returns counts of rules and targets, not the rules or the
  targeted users themselves, and cuts each variation value at 128 bytes.
- Summaries and comments in the audit log are written by people and
  integrations, so they are passed to the model as untrusted text, like
  every source result.

## Access token

Create an access token for Sidekick under **Organization settings**,
**Authorization**, **Tokens**, **Create token**:

- **Role.** Give it the **Reader** base role. Sidekick only sends `GET`
  requests to the audit log (`/api/v2/auditlog`) and feature flag
  (`/api/v2/flags/<project>/<flag>`) endpoints, so read access is all it
  needs.

  On a plan with custom roles, you can narrow it to the projects in
  `projects` with an inline policy or a custom role that allows only
  `viewProject` on them:

  ```json
  [
    {
      "effect": "allow",
      "actions": ["viewProject"],
      "resources": ["proj/shop"]
    }
  ]
  ```

- **Service token.** On Enterprise plans, tick **This is a service token**,
  so the token does not belong to a person and keeps working when they
  leave.
- **API version.** Any version works, since Sidekick sends its own.

Put the token in the Secret that `tokenSecret` names:

```sh
kubectl create secret generic sidekick-launchdarkly --from-literal=token=api-...
```

LaunchDarkly counts requests against rate limits per token, per route and
per account, every ten seconds. Sidekick's calls are few, but a token
shared with other tools can run out; give Sidekick its own.

LaunchDarkly also runs a hosted MCP server, which signs in with OAuth only
and is not available on the EU or Federal instances, and a local MCP
server that wraps the whole API, including the calls that change flags.
This source reads only what an investigation needs, with a token that
cannot change anything.
