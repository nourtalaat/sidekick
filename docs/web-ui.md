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

## Admin operations

- `GET /api/v1/config` returns the config Sidekick runs with: `file`, the
  config file with its defaults, which only a config change can alter,
  and `runtime`, the listeners and routes changed at runtime, with their
  version and who changed them last. Secrets appear only as references
  to a Secret's name and key, never as values. In case a credential was
  written into the file by mistake, the user and password of every URL
  are removed, and strings the [redaction](configuration.md#redaction)
  rules match are redacted.
- `GET /api/v1/jobs?status=dead` lists dead jobs, the background jobs
  that gave up, newest first, 50 at a time (`limit` takes up to 100);
  pass the answer's `nextCursor` as `cursor` for the next page. Each
  job's last error is masked the same way.
- `POST /api/v1/jobs/{id}/retry` sets a dead job back to pending with its
  attempts reset. The retry is written to the audit log. A job that is
  not dead, or whose unique key a pending or running job holds, answers
  409.

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
| 409 | The change conflicts with the current state, such as retrying a job that is not dead. |
| 422 | A body that is valid but names something that is not there, such as a finding the report does not have. |
| 404, 405 | No operation has this path, or this method on it. |
