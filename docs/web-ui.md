# Web UI and API

Sidekick's web UI and its JSON API need a login through your OpenID
Connect (OIDC) provider, such as Okta, Microsoft Entra ID, Google or
Keycloak. They are served on `server.listen` only when the config has an
[`oidc` section](configuration.md#oidc); without it, there is no web UI,
no `/auth` endpoints and no `/api/v1`.

The UI is part of the Sidekick binary and image; it needs nothing else
installed, and it loads no script, style or font from another host. It is
served at `/` with a strict Content Security Policy (`default-src 'self'`,
no inline scripts or styles), `X-Content-Type-Options: nosniff` and
`Referrer-Policy: same-origin`. A reverse proxy in front of Sidekick must
not replace those headers.

## Register Sidekick at your provider

1. Create a web application (a confidential client that uses the
   authorization code flow) for Sidekick.
2. Set its redirect URI to `<server.publicURL>/auth/callback`, for
   example `https://sidekick.example.com/auth/callback`. Sidekick must be
   reached over https; the session cookie is never sent over plain http.
3. Have the provider put the user's groups in the ID token, and note the
   claim name (usually `groups`). Some providers send groups only when a
   scope asks for them; add that scope to `oidc.scopes`.
4. Put the client secret in a Secret and reference it from
   `oidc.clientSecret`.

```yaml
server:
  publicURL: https://sidekick.example.com

oidc:
  issuer: https://acme.okta.com
  clientID: sidekick
  clientSecret: { name: sidekick-oidc, key: clientSecret }
  groupsClaim: groups
  roles:
    admin: [sre-admins]
    approver: [oncall-leads]
    responder: [engineering]
    viewer: ["*"]
```

Sidekick reads the provider's discovery document at the first login, not
at start-up, so it starts while the provider is down.

## Roles

`oidc.roles` maps each role to the groups that give it. A user gets the
highest role any of their groups gives. A user no group matches cannot
sign in. The role is read again at every login, so a change in the
provider takes effect at the user's next login.

`"*"` matches every user who can sign in at the provider, so Sidekick
allows it only for `viewer`: one typo must not make every employee an
admin. With Google (`https://accounts.google.com`) it is refused
altogether, because anyone with a Google account, inside your company or
not, can sign in there. Google sends no groups claim of its own, so
Google sign-in needs something in front of it, such as a federating
provider, that adds the user's groups in a custom claim; name that claim
in `oidc.groupsClaim`.

| Role | Can |
|---|---|
| `viewer` | See investigations, steps, dashboards, lessons, strategies and benchmark results. |
| `responder` | Everything a viewer can, plus give feedback, ask follow-ups, stop and resolve investigations, and edit listeners. |
| `approver` | Everything a responder can, plus approve or reject actions. |
| `admin` | Everything an approver can, plus edit routes, strategies, lessons and memory, review scenarios, and see the audit log, the effective config and dead jobs. |

A user's first login, and a login that changes their role, are written to
the audit log.

## Users and email

Sidekick keeps each user by the provider's issuer and subject, never by
email. It keeps the user's email only when the provider does not mark it
unverified: a token without the `email_verified` claim counts as
verified, since some providers, such as Microsoft Entra ID, never send
it. One user holds an email at a time; when a newer login has an email
another user holds, the email moves to the newer user and Sidekick logs
a warning with both user IDs.

## Slack users

With the `oidc` section set, a Slack user whose email a web UI user has
gets that user's role in Slack too, once the web UI user has signed in at
least once. [Roles in Slack](slack.md#roles) has the details.

## Sessions

A login starts a session that lasts 12 hours and is not extended by use;
after it ends, the user signs in again. The session cookie is `Secure`,
`HttpOnly` and `SameSite=Lax`, and the database keeps only a hash of the
session ID. `POST /auth/logout` ends the session.

Every request made with a session that can change something (any method
but `GET`, `HEAD` and `OPTIONS`), logout included, must send the
session's CSRF token in the `X-CSRF-Token` header. `GET /api/v1/me`
returns it as `csrfToken`.

## API tokens

Automation, such as a CI job that uploads benchmark results, calls the
API with an API token instead of a session:

```sh
curl -H "Authorization: Bearer $SIDEKICK_TOKEN" https://sidekick.example.com/api/v1/me
```

An admin signed in to the web UI creates tokens with
`POST /api/v1/tokens`, giving a name, a role and an expiry:

```json
{"name": "nightly bench", "role": "viewer", "expiresAt": "2027-04-01T00:00:00Z"}
```

- The answer holds the token itself (it starts with `skt_`) in `secret`.
  It is shown only once; Sidekick keeps only a hash of it.
- The role is at most the admin's own, and the expiry at most a year
  away.
- Only a signed-in user can create or revoke tokens; a token cannot,
  even an admin one.
- `GET /api/v1/tokens` lists every token with when it was last used, to
  the minute, and `DELETE /api/v1/tokens/{id}` revokes one at once.
- Requests with a token need no CSRF token.
- Creating and revoking a token are written to the audit log.

## Investigations

Every role from `viewer` up can read investigations:

- `GET /api/v1/investigations` lists them, newest first, 50 at a time
  (`limit` takes up to 200); pass the answer's `nextCursor` as `cursor`
  for the next page. The filters `from`, `to`, `route`, `service`,
  `model`, `verdict` and `status` must all match, and `q` matches the
  words of the alert title (`"quotes"` match a phrase, `-word` leaves a
  word out). Each row has the verdict, status, cost, how the verdict was
  rated (`correct`, `wrong`, `mixed` or `none`) and the state of the
  newest watcher.
- `GET /api/v1/investigations/{id}` returns one investigation with its
  report, the first 50 events of its group and the follow-up questions no
  run has answered yet.
- `GET /api/v1/investigations/{id}/steps` returns the record of the run,
  100 steps at a time (`limit` takes up to 500), with `run` and `after`
  to choose which. The tool results are redacted already. Text over 32
  KiB is cut in the answer (marked `truncated`); the database keeps all
  of it. A reasoning step shows the text the provider returned, never
  its signature.
- `GET /api/v1/investigations/{id}/watchers` returns the watchers of the
  investigation's group with their last 50 checks.
- `GET /api/v1/investigations/{id}/stream` is a stream of server-sent
  events: `step` for each new step, `status` when the status, the run,
  the stop request or the confirmed verdict changes, and `watchers` when
  a watcher changed. Any replica serves it, not only the one that runs
  the investigation, because each replica listens for changes in the
  database. It sends a comment every 15 seconds and ends after 30
  minutes; the browser reconnects with `Last-Event-ID`, and the stream
  resumes after that step. A replica serves at most 100 streams at once
  and answers 429 above that. The session or token is checked when the
  stream opens, so a revoked one can keep a stream for up to 30 minutes.

## Actions on investigations

These need the `responder` role or higher, and a session's CSRF token
(an API token needs none). A token can call them too; the audit log then
names the token. Each action writes its audit row in the same transaction
as the change.

- `POST /api/v1/investigations/{id}/follow-ups` with `{"text": "..."}`
  asks a follow-up question, as a thread reply does in Slack. The text
  is trimmed, redacted and at most 4000 bytes. The answer is 202. A
  triage-only result and an investigation that failed take no questions
  and answer 409; for a triage-only result, start a full investigation
  with **Investigate anyway** in Slack first. The audit row keeps the
  question's length, never its text.
- `POST /api/v1/investigations/{id}/cancel` asks the run to stop, as the
  Stop button does in Slack. `requested` in the answer is `false` when
  the investigation has ended or a stop was asked for already.
- `POST /api/v1/investigations/{id}/resolve` marks the investigation's
  group as resolved and ends its watchers, as a recovery does: watchers
  that wait for the alert to clear count as recovered, the others stop.
  `resolved` in the answer is `false` when the group was resolved
  already. A group that closed because its window ended answers 409.
  `groupStatus` and `groupClosedReason` in the investigation say which
  groups are still open.
- `POST /api/v1/watchers/{id}/stop` stops a running watcher, as **Stop
  watching** does in Slack. `stopped` in the answer is `false` when the
  watcher had ended already. The watchers of an investigation name who
  stopped one in `stoppedBy`: the user's or token's ID and name, or the
  Slack user's ID.

The list's service, model and title columns are filled when an
investigation is created. Investigations from before they existed (older
than this version) are filled in the background in batches of 5,000 when
Sidekick starts; until that job has finished, they do not match the
`service`, `model` and `q` filters.

### Feedback

`POST /api/v1/investigations/{id}/feedback` needs the `responder` role and a
signed-in user: an API token gets 403, since feedback comes from a person.
It says that one part of an investigation is correct or wrong:

```json
{
  "targetKind": "finding",
  "targetRef": "run:2/finding:f2",
  "rating": "wrong",
  "reason": "No deploy happened at that time.",
  "correctVerdict": "false_positive",
  "correctCause": "A load test."
}
```

- `targetKind` is `verdict`, `finding`, `fix` or `tool_call`, and
  `targetRef` names the part: `run:N/verdict`, `run:N/finding:ID` or
  `run:N/fix:ID`, where N is the investigation's current run, or the ID
  of a tool call. A part that is not in the current report or steps
  answers 422, so a page that still shows an older run learns that its
  report is gone. A verdict, finding or fix needs an investigation that
  has a verdict (409 otherwise); a tool call can be rated while the
  investigation runs.
- `rating` is `correct` or `wrong`. `reason` and `correctCause` take up
  to 2,000 characters. `correctVerdict` is the real verdict, one of the
  verdicts of a report; it may be given on any part. On the verdict, a
  `correct` rating cannot name another verdict than the reported one, and
  a `wrong` rating cannot name the reported one (422).
- Each person has one opinion per part: a new submission replaces the
  earlier one. The answer is the stored feedback, with `confirmation`,
  which says how it changed the confirmed verdict of the investigation:
  `confirmed`, `revised`, `withdrawn` or `unchanged`.
- A real verdict (`correctVerdict`) confirms the investigation, whichever
  part the feedback is about, and puts it in memory. The cause is
  `correctCause`, or the report's cause when the real verdict is the
  reported one. A `correct` rating of the verdict confirms it too, and a
  `wrong` rating takes back a confirmation of that verdict, as the buttons
  in Slack do. Feedback that disagrees with the confirmed verdict
  replaces it. Every change made with a real verdict, and every revision
  or withdrawal, is written to the audit log with the user who made it.

## Investigation pages

The web UI shows the investigations in two pages, which every role from
`viewer` up can open.

- **The list** (`/investigations`) has a row for each investigation,
  newest first, with the verdict first so the list can be scanned: the
  alert title, service, route, start time, duration, cost, how people
  rated the verdict, and the state of the newest watcher. The filters
  above the table are kept in the address (`q`, `verdict`, `status`,
  `range`, `route`, `service` and `model`), so a view can be shared.
  **Load more** adds the next page. **Compact rows** (`compact=1`) makes
  the rows shorter.
- **The investigation page** (`/investigations/<id>`, the address the
  Slack link opens) shows the verdict, how sure Sidekick is, the reason,
  the state of the watchers, the report, what was checked, the cost and
  the model, the alerts of the group, and the record of the run, one line
  for each step. A step opens to show the tool call or result in a box
  with a copy button.

A `responder` or higher also sees **Stop**, **Resolve**, **Stop watching**
and the follow-up box at the bottom of the page. A `viewer` does not see
these buttons. The server checks the role of every request again, and
the page shows its reason when it refuses one.

While a run goes, the page follows the investigation's stream and adds
the new steps in batches every few seconds, so a screen reader announces
them one batch at a time. The stream also opens when a follow-up question
waits for its run, and when the page reads the investigation again (every
30 seconds while nothing runs) and finds a new run, such as an answer to
a question asked in Slack.

Text from alerts, sources and the model is always shown as plain text,
never as HTML. A link opens only when its address is `http` or `https`;
any other address, such as `javascript:`, is shown as text.

`GET /api/v1/investigations/{id}/feedback` needs only the `viewer` role. It
lists the parts of the current run that people rated: the verdict, each
finding and fix, and each tool call, with how many people rated each one
`correct` and `wrong`, from Slack and the web UI together. For a signed-in
caller it also has `mine`, the caller's own opinion of the part, with the
reason and the real verdict and cause they gave. Parts nobody rated, and
the parts of an earlier run, are left out.

## Listeners and routes

The listeners and routes that people add at runtime, and those of the
config file, are under `/api/v1/listeners` and `/api/v1/routes`. A
`viewer` can read both; a `responder` can add, replace and delete
listeners; an `admin` can do the same for routes. Every change runs the
checks the config file and the `/sidekick` commands run, is written to
the audit log with the old and new values in the same transaction, and is
used by every replica within seconds.

- `GET /api/v1/listeners` and `GET /api/v1/routes` list every rule. Each
  has a `layer`, `file` or `runtime`, and a `status`: `active`;
  `shadowed`, a runtime rule with the name of a rule in the config file,
  which wins; `invalid`, a runtime rule that no longer passes the checks,
  with the reason in `statusReason`; or `disabled`. Rules in effect come
  first, routes in the order they are tried (`position`), then the runtime
  rules that are not in effect. A rule of the config file is `readOnly`.
- `POST` adds a rule with its `name`, and `PUT /{name}` replaces a runtime
  rule and turns it on or off with `enabled`. The body has the keys the
  rule has in the config file: for a listener `channel`, `source`,
  `match`, `extract` and `recovery`; for a route `priority`, `match`,
  `strategy`, `sources`, `outputs`, `mode`, `budgets` and `grouping`.
  Durations are written as `5m`. A route needs a `match`, uses only the
  sources, outputs and strategies of the config file, keeps its budgets at
  or below the global ones, and has the mode `read-only`, the only mode
  there is for now. A runtime listener is never trusted.
- `PUT` takes the `version` of the rule that was read. If the rule has
  changed since, the answer is 409 with the code `stale`.
- `DELETE /{name}` removes a runtime rule. It also removes a runtime rule
  that a rule of the config file shadows, which nothing else can remove.
- A rule that fails the checks answers 422 with the code `validation` and
  an `errors` list, one entry for each problem, naming the `field` of the
  body, such as `budgets.maxToolCalls` or `match.sender.bots[0]`. A rule of
  the config file answers 409 with the code `file_rule`, a name that is
  taken 409 with `exists`, and an unknown name 404 with `not_found`.

## Admin operations

- `GET /api/v1/config` returns the config Sidekick runs with: `file`, the
  config file with its defaults, which only a config change can alter,
  and `runtime`, the listeners and routes changed at runtime, with their
  version and who changed them last. Secrets appear only as references
  to a Secret's name and key, never as values. In case a credential was
  written into the file by mistake, the user and password of every URL
  are removed, and strings the [redaction](configuration.md#redaction)
  rules match are redacted.
- `GET /api/v1/audit` returns the audit log, newest first, 50 entries at
  a time (`limit` takes up to 200); pass the answer's `nextCursor` as
  `cursor` for the next page. The filters `from` (inclusive), `to`
  (exclusive), `actor`, `action` and `investigation` must all match.
  `actor` is the ID of a user, an API token or a Slack user; `sidekick`,
  `policy` and `system` select what Sidekick did on its own. Each entry has
  the actor, with the name of the user or token when it is still known,
  the action, what it was done to, and for a change the `old` and `new`
  values, masked the same way as the effective config. Rows are never
  changed.
- `GET /api/v1/jobs?status=dead` lists dead jobs, the background jobs
  that gave up, newest first, 50 at a time (`limit` takes up to 100);
  pass the answer's `nextCursor` as `cursor` for the next page. Each
  job's last error is masked the same way.
- `POST /api/v1/jobs/{id}/retry` sets a dead job back to pending with its
  attempts reset. The retry is written to the audit log. A job that is
  not dead, or whose unique key a pending or running job holds, answers
  409.

## Listeners, routes and the audit log in the UI

The navigation has a page for each:

- **Listeners** (`/listeners`) and **Routes** (`/routes`) list the rules of
  the config file and those added at runtime. Each row says where the rule
  is defined and its status: active, shadowed (a rule of the config file
  has the same name and is used instead), invalid (it no longer passes the
  checks, with the reason) or turned off. Routes show the order they are
  tried in. Rules of the config file have no buttons; only a config change
  alters them. A shadowed runtime copy can be deleted.
- **Add**, **Change** and **Delete** are shown to a responder for
  listeners and to an admin for routes; a viewer sees the lists only.
  Delete asks first. A save runs the same checks as the config file and
  the `/sidekick` commands, and the form shows each problem under its
  field, with problems that belong to no field in a message at the top.
  The mode of a route can only be read-only for now. If someone else
  changed the rule while the form was open, the form says so and offers
  to load the new version.
- **Audit log** (`/audit`, admins) shows the entries newest first, with
  filters for the time, who, the action and the investigation. The
  filters are in the address, so a view can be shared with a link. An
  entry with an old or new value has a **Show change** button.

## The JSON API

The API is under `/api/v1`, described in OpenAPI 3.1 in
[`api/openapi.yaml`](../api/openapi.yaml). Each operation names the lowest
role that may call it in `x-sidekick-role`. Errors are problem details
(RFC 9457), with `Content-Type: application/problem+json`:

```json
{"type": "about:blank", "title": "Unauthorized", "status": 401, "detail": "Sign in, or send an API token.", "instance": "/api/v1/me"}
```

| Status | Means |
|---|---|
| 400 | A parameter or body that is not valid. |
| 401 | No session or API token, or one that expired or was revoked. |
| 403 | A change without the CSRF token, or a role below the one the operation needs. |
| 409 | The change conflicts with the current state, such as retrying a job that is not dead, or changing a rule of the config file. |
| 422 | A body that is valid but names something that is not there, such as a finding the report does not have, or a listener or route that fails the config checks (the answer lists the fields). |
| 404, 405 | No operation has this path, or this method on it. |
