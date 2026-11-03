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

## The pages of the web UI

The menu on the left shows the pages below. The role is the lowest role that
can use the page. For a page that a lower role can read, the role to change
things is named too. The server checks the role of every request.

| Page | Address | Lowest role |
|---|---|---|
| [Investigations](#investigation-pages) | `/investigations` | `viewer` |
| [One investigation](#investigation-pages) | `/investigations/<id>` | `viewer`; **Stop**, **Resolve**, **Stop watching**, the fix mark and follow-up questions need `responder` |
| [Dashboard](#the-dashboard) | `/dashboard` | `viewer` |
| [Review queue](#the-review-queue), tab **Lessons** | `/review?tab=lessons` | `viewer` reads; `admin` decides |
| Review queue, tab **Strategies** | `/review?tab=strategies` | `admin` |
| Review queue, tab **Scenarios** | `/review?tab=scenarios` | `admin` |
| Review queue, tab **Actions** | `/review?tab=actions` | `approver` |
| [Lessons](#the-lessons-and-memory-page), tab **Lessons** | `/lessons?tab=lessons` | `viewer` reads; `admin` changes |
| Lessons, tab **Memory** | `/lessons?tab=memory` | `viewer` reads; `admin` changes |
| Lessons, tab **Service notes** | `/lessons?tab=notes` | `viewer` reads; `admin` changes |
| [Routes](#listeners-routes-and-the-audit-log-in-the-ui) | `/routes` | `viewer` reads; `admin` changes |
| [Strategies](#strategies) | `/strategies` | `viewer` reads; `admin` changes |
| [Listeners](#listeners-routes-and-the-audit-log-in-the-ui) | `/listeners` | `viewer` reads; `responder` changes |
| [Benchmarks](#the-benchmarks-page) | `/benchmarks` | `viewer` |
| [One benchmark run](#the-page-of-one-benchmark-run) | `/benchmarks/<id>` | `viewer` |
| [Comparing two runs](#comparing-two-benchmark-runs) | `/benchmarks/compare` | `viewer` |
| [Audit log](#listeners-routes-and-the-audit-log-in-the-ui) | `/audit` | `admin` |
| [Settings](#settings), tab **Config** | `/settings?tab=config` | `admin` |
| Settings, tab **API tokens** ([details](#api-tokens)) | `/settings?tab=tokens` | `admin` |
| Settings, tab **Failed jobs** | `/settings?tab=failed-jobs` | `admin` |

A viewer, a responder and an approver see the first eight items in the menu
(Investigations, Dashboard, Review queue, Lessons, Routes, Strategies,
Listeners and Benchmarks); an admin also sees Audit log and Settings. [The
menu](#the-menu) says what a role below the page sees.

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

## Theme

The theme switch in the header chooses System, Light or Dark. The choice is
saved for the signed-in user on the server, so it follows the person to
every browser and survives clearing site data. The browser also keeps a copy
in `localStorage`, so the page does not flash the wrong theme before the
server answers. After sign-in, the server's choice wins over the copy. A
person who has no saved choice yet keeps the one their browser holds, which
is saved at the first sign-in after an upgrade. If a save fails, the page
keeps the choice on screen and the next change tries again.

The login page has no session, so it uses the choice the browser holds, or
the operating system's setting. `PUT /api/v1/me/theme` with
`{"theme": "dark"}` saves the choice and `GET /api/v1/me` returns it as
`theme`. Only a session can use them: an API token has no theme and gets
403. A value other than `system`, `light` or `dark` gets 422. Saving a theme
writes no audit row.

## API tokens

Automation, such as a CI job that uploads benchmark results, calls the
API with an API token instead of a session:

```sh
curl -H "Authorization: Bearer $SIDEKICK_TOKEN" https://sidekick.example.com/api/v1/me
```

An admin makes and revokes tokens on the **API tokens** tab of Settings
(`/settings?tab=tokens`):

1. Enter a name for what the token is for, 1 to 100 characters.
2. Choose a role. The list holds your own role and every role below it, so an
   admin can choose any of the four.
3. Choose when it expires: in 30, 90, 180 or 365 days, or on a date you pick.
   The token works until the end of that day. The latest date is a year from
   today; a later date is refused before anything is sent.
4. Press **Create token**. The page shows the token, which starts with `skt_`,
   with a **Copy** button. Copy it now: it is not shown again. It is held only
   on the page, never in the browser's storage or the address, and it is gone
   when you press **Done**, create another token or leave the tab.
5. To stop a token, press **Revoke** on its row and confirm. Anything that uses
   it stops working at once. The button is gone once a token is revoked.

The list shows each token's name, role, when it was created and the ID of the
user who created it, when it expires, when it was last used (to the minute, or
"Never") and its status. Tokens that work come first, then expired and revoked
ones, each group with the newest first. Expired and revoked rows are dimmed and
say **Expired** or **Revoked** in words; a revoked row also shows when.

The form calls `POST /api/v1/tokens` as the signed-in admin, with a name, a
role and an expiry:

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
- `PUT /api/v1/investigations/{id}/fix-applied` records that a responder
  applied the fix of a false positive, such as a new monitor threshold.
  The dashboard counts it under noisy alerts fixed, and the investigation
  shows `fixAppliedAt`. Only an investigation that ended with the verdict
  `false_positive` has a fix to mark; any other answers 409. `DELETE` on
  the same path takes the mark back. Both answer `applied`, `changed` and
  `appliedAt`; a call that finds the mark as it asks for answers 200 with
  `changed` false and writes no audit row. The audit actions are
  `fix.applied` and `fix.unapplied`.
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
- A `reason` also starts a lesson when `model.roles.summarize` is set:
  Sidekick asks that model to draft one from the feedback (see
  [`lessons`](configuration.md#lessons)). A reason you change later does
  not draft a second lesson from the same feedback.
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
- **The search box** in the header searches the words of the alert title,
  with the same forms as the `q` filter: `"quotes"` for a phrase and
  `-word` to leave a word out. Press Enter to open the list with `q` set
  (at most 200 characters); the other filters of the list are dropped, and
  an empty box opens the list with no filters. Typing changes nothing
  until Enter. While the list is open, the box shows the same text as the
  list's **Alert title** field.
- **The investigation page** (`/investigations/<id>`, the address the
  Slack link opens) shows the verdict, how sure Sidekick is, the reason,
  the state of the watchers, the report, what was checked, the cost and
  the model, the lessons the prompt used, the memory entries it showed as
  examples, the alerts of the group, and the
  record of the run, one line for each step with its time, tokens, cost
  and model. A step opens to show the model and the connector it ran
  through, then the tool call or result in a box with a copy button. The
  prompt step, and the triage prompt step, also say how many lessons they
  used and which lessons applied but were left out, with the reason; see
  [Lessons](#lessons).

Under **Lessons used**, the **Memory used** card lists the past investigations
that the prompt of the current run showed as examples, in the order of the
prompt, up to 10. Each has its confirmed verdict, its alert title (or its
summary) as a link to its investigation, and why it was chosen: "the same alert
fingerprint", "the same service and alert name", "similar words" or "a similar
description", as the prompt words it. An entry that was deleted since says "No
longer in memory", and one that was disabled says "Disabled since this run".
The card says that the entries are shown as they are now, and that the prompt
had their text at the time. The card is left out when the run used no examples.
An `admin` also sees **Manage memory**, a link to the Memory tab of the
[Lessons and memory page](#the-lessons-and-memory-page).

Under the watchers, each watcher has a **Checks** list that opens to show
how the watcher started, how it ended and when, and its last checks, newest
first: the time with seconds, whether the check passed, failed or had an
error, what the checked service answered, and how long the check took. The
list starts open for a watcher that is still running and closed for one that
ended; after that the page keeps your choice. It shows up to 50 checks; when more ran, it says "Showing the last
50 of N checks." A long answer is cut after a few lines until **Show all**
is pressed. The answer comes from the checked service, so it is shown as
plain text. The list sits outside the watcher box, so a screen reader does
not read out every new check; it is read again every 60 seconds and when the
stream reports that a watcher changed.

Under the verdict, each finding, each fix and each tool result, the page
says how many people rated that part correct and how many wrong, from
Slack and the web UI together. A `responder` or higher also sees
**Correct**, **Wrong** and **Add a reason** there, and their own rating is
shown as pressed. **Correct** is sent at once. **Wrong** opens a form for
the reason; on the verdict it also asks for the real verdict and cause,
and giving the real verdict confirms the investigation. Sending again
replaces your earlier opinion of the part. If the report changed since the
page was read, for example because a follow-up run replaced it, the form
shows the reason the server gave and keeps what you typed. The counts are
read again every 30 seconds and when a run changes the investigation.

Every role has a **Copy link** button. A `responder` or higher also sees **Ask a
follow-up**, which moves to the follow-up box at the bottom of the page, **Stop**
while a run goes, **Resolve** while the group is open, and **Stop watching**.
An `admin` sees **Export as scenario** on an investigation whose verdict
someone confirmed. An investigation that ended as a false positive has a
**Fix** section that says "Fix applied" or "Fix not applied yet"; a `responder`
or higher can **Mark fix as applied** or **Take back the mark**. A `viewer`
does not see these buttons. The server checks the role of every request again, and
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

## Lessons

Lessons are short pieces of guidance, up to 400 characters, that prompts show
the model; see [`lessons`](configuration.md#lessons). Anyone with the
`viewer` role can read them, and an `admin` changes them. All of it is
under `/api/v1`:

- `GET /lessons` lists lessons newest first, a page at a time (`limit`, at
  most 200, and `cursor`). `status` (`proposed`, `active`, `disabled` or
  `rejected`), `service` and `q`, words the text matches as in a web
  search, narrow the list. The answer also has `counts`, how many lessons
  have each status whatever the filters. A lesson has its text, its scope
  (`service`, `alert`, `route` and `strategy`; a lesson with none applies to
  every investigation), its status and origin (`feedback`, `ui`, `slack` or
  `file`), the feedback a drafted lesson comes from, how many people rated
  the verdicts of investigations that used it correct (`helpful`) and wrong
  (`unhelpful`), and a `version`.
- `POST /lessons` writes a lesson, active at once or with `status:
  proposed`. A lesson with no scope field is global.
- `PUT /lessons/{id}` replaces the text and scope, and may set `status`: a
  lesson that waits for review can be rejected, an active one disabled, a
  disabled one enabled. A lesson drafted from feedback keeps at least one
  scope field. `POST /lessons/{id}/approve` makes a lesson that waits for
  review active and notes who approved it. `DELETE /lessons/{id}` deletes
  one, with the record of the investigations that used it.
- An edit or approval sends the `version` it read (400 without one). If the
  lesson changed since, the answer is 409 with code `stale`; approving a lesson that does
  not wait for review is 409 with code `not_proposed`. A text or scope that
  fails the checks is 422 with the field. Every change is written to the
  audit log.
- `GET /investigations/{id}` lists the `lessons` the investigation used, with
  their text and status as they are now. A prompt step in
  `GET /investigations/{id}/steps` has `lessons`: the ones it used and the
  first 50 it left out, each with the reason, and how many applied.

Secrets in the text of a lesson are redacted when it is saved, as in feedback. The text of a lesson came from people and the model: show it as plain text.

## Memory entries and service notes

Memory keeps one entry for each investigation whose verdict a responder
confirmed; see [`memory`](configuration.md#memory). An admin can fix an entry
that misleads, or switch it off. Anyone with the `viewer` role can read
entries and notes, and an `admin` changes them. All of it is under `/api/v1`:

- `GET /memory/entries` lists entries, newest confirmation first, a page at
  a time (`limit`, at most 200, and `cursor`). `service`, `verdict`, `q`
  (words from the service, alert name, summary and cause, as in a web
  search) and `disabled` (`true` for only disabled entries, `false` for only
  enabled ones) narrow the list. An entry has its investigation ID (the
  investigation may be gone, since memory outlives investigations), service,
  alert name, confirmed verdict, summary, cause, when it was confirmed and
  whether it is disabled. The list has no total.
- `GET /memory/entries/{id}` reads one entry; the ID is the investigation's.
- `PUT /memory/entries/{id}` replaces the `summary` (required, up to 2,000
  characters), the `cause` (up to 2,000, empty for none) and the `disabled`
  flag, and answers with the entry as stored. There is no version: the last
  write wins. A disabled entry is never shown to a prompt or found by a
  search. A request that changes nothing writes nothing.
- `DELETE /memory/entries/{id}` deletes an entry and its embedding.
- `GET /services/notes` lists the services that have notes, by name, a page
  at a time (`limit`, at most 500, and `after`, the `nextAfter` of the page
  before), with the first 120 characters of each and who changed them last.
  `GET /services/{name}/notes` reads the notes about one service, up to
  4,000 characters of plain text; a service without notes answers 200 with
  empty `text`. `PUT /services/{name}/notes` replaces them, and empty text
  deletes them. A name with a slash in it is sent as `%2F`.
- A summary, cause or notes text that fails the checks is 422 with the
  field. An entry that does not exist is 404. Text you send is redacted
  before it is stored, as for lessons, and the length checks run on the
  redacted text.

Two things to know about editing memory:

- **An edit lasts until the confirmation changes.** When someone changes the
  confirmed verdict or cause of the investigation, Sidekick writes the
  entry again from the investigation: the summary and cause you edited are
  replaced. The `disabled` flag stays.
- **A delete lasts until the confirmation changes too.** The next change
  of the investigation's confirmation writes a new entry. To keep an entry
  out for good, disable it instead of deleting it.

With vector memory on, an edit of the summary or cause embeds the entry
again.

Every change is written to the audit log with the old and new values, as
`memory.updated`, `memory.disabled`, `memory.enabled`, `memory.deleted`,
`service_notes.updated` or `service_notes.deleted`. The memory entries have
`target_kind` `memory_entry` and the investigation filter of the audit log
finds them; notes have `target_kind` `service_notes` and the service as the
target.

## The Lessons and memory page

The **Lessons** item of the navigation (`/lessons`) is one page with three
tabs: **Lessons**, **Memory** and **Service notes**. The tab is in the address
(`/lessons?tab=memory`), so a link opens the page on it, and the arrow keys
move between the tabs. Everyone who can sign in can read the page; changing
anything needs the `admin` role. Text on the page is plain text, never HTML.

The **Lessons** tab lists every lesson, newest first, a page at a time. Filters
for the status, the service and words in the text narrow the list, and stay in
the address. Each lesson shows its status, where it applies, how many times
people rated the verdict of an investigation that used it correct (found
helpful) and wrong (found unhelpful), and when it was last used. An `admin` can:

- **Write a lesson.** With no scope field filled in, the lesson is global and
  applies to every investigation.
- **Edit** the text and scope of an active or disabled lesson.
- **Disable** an active lesson, so prompts stop using it, and **enable** a
  disabled one.
- **Delete** a lesson. The page asks first, in the page and not in a browser
  dialog. The record of the investigations that used it goes with it; the audit
  log keeps its text.

A lesson that waits for review links to the [review queue](#the-review-queue),
where it is approved or rejected.

The **Memory** tab lists the memory entries, newest confirmation first, with
filters for the service, the verdict, words and whether an entry is disabled.
Each entry shows its verdict, summary, cause, service and alert name, when it
was confirmed and a link to its investigation. An `admin` can edit the summary
and cause, disable or enable the entry, and delete it. Two things to know:

- An edit lasts until the confirmation of the investigation changes. Then
  Sidekick writes the entry again from the investigation, and the text you wrote
  is replaced; the disabled state stays.
- A delete lasts until the confirmation changes too: Sidekick then writes a new
  entry. To keep an entry out of prompts for good, disable it.

An investigation page lists the entries its prompt showed in its
[Memory used card](#investigation-pages). The card reads the entries as they
are now, so an edit, a disable or a delete shows there, while the prompt had
the text at the time.

The edit form and the delete step say so. See
[Memory entries and service notes](#memory-entries-and-service-notes) for the
API.

The **Service notes** tab lists the services that have notes. Pick one, or type
a service name, to read its notes, see who changed them last and when, and, as
an `admin`, edit them as plain text up to 4,000 characters. Saving empty text
deletes the notes, after the page asks. The memory source shows the notes to the
model; see [the memory source](sources/memory.md).

Every change from this page is written to the audit log.

## The review queue

The **Review queue** (`/review`) is one page with a tab for each kind of
thing that waits for a person. The tab is in the address
(`/review?tab=lessons`), so a link opens the queue on it, and the arrow keys
move between the tabs. The tabs are **Lessons**, **Strategies**, **Scenarios**
and **Actions**. Lessons can be read by every role and decided by an `admin`;
Strategies and Scenarios are for an `admin`; Actions is for an `approver`, and
a lower role sees a note that says so.

The **Lessons** tab lists the lessons drafted from feedback that wait for
review, newest first, each with its text, where it applies and links to the
investigations whose feedback it came from. Everyone who can sign in can read
it. An `admin` can:

- **Approve** the lesson, so prompts use it from now on.
- **Edit** its text, up to 400 characters, and its scope (service, alert
  name, route and strategy). A lesson drafted from feedback keeps at least
  one scope field. The lesson stays in the queue until it is approved or
  rejected.
- **Reject** it. The lesson is kept, with the status `rejected`, and never
  used.

If someone else changed the lesson while the page was open, the page says
so and the queue shows the new version. The text of a lesson is shown as
plain text, never as HTML.

### Proposed strategy changes

The **Strategies** tab lists the changes Sidekick drafted to strategies after
repeated wrong feedback. See [`strategies.proposals`](configuration.md#proposed-changes)
for what turns them on and what they cost. Only an `admin` can read them; a
lower role sees a message that names the `admin` role, and the tab sends no
request.

Each proposal shows its status: being drafted, draft failed, waiting for the
gate, gate running, passed the gate, did not pass, applied, approved (for a
strategy in a file) or rejected. Opening one shows:

- why the model changed the strategy, in its own words, which is model output;
- the change as a diff of the strategy before and after;
- the wrong ratings it was drafted from, with their reasons and links to the
  investigations;
- the result of the [benchmark gate](benchmark.md#the-strategy-gate): what it
  means and what to do about it, the mean difference of the scores with its 95%
  interval against the allowed drop, the score of each scenario with the
  current version and with the change, and what the gate cost.

An admin can then:

- **Approve** a proposal that passed. For a runtime strategy the change
  becomes its next version, and for a built-in strategy a runtime strategy of the
  same name that takes its place; the version records the proposal and the
  benchmark run that checked it. Sidekick cannot change a strategy file, so for
  one the page gives the YAML to download or copy and you commit it. The button
  is off for any proposal that has not passed, and the server refuses it too:
  there is no way around the gate.
- **Edit** a proposal whose gate has ended. The new text goes through the same
  checks as a saved strategy, the old gate result is dropped, and the proposal
  goes through the gate again. Saving the same text runs the gate again, which
  is the way to retry a gate that could not finish.
- **Reject** a proposal, with a reason if you like. A gate that is still
  running stops before its next run.

Approving is refused with `stale` when the strategy was saved again since the
draft; reject the proposal or edit it to run the gate on the strategy as it is.
Everything an admin does is written to the audit log in the same transaction.

The API behind the tab, all for an `admin`:

- `GET /api/v1/strategy-proposals` lists them, newest first, 50 at a time
  (`limit` up to 200, `cursor` from the answer's `nextCursor`), filtered by
  `status` or `name`. Without a `status` the proposals that are still being
  drafted, and failed drafts, are left out.
- `GET /api/v1/strategy-proposals/{id}` returns one with `baseBody` and `body`,
  its feedback, its `gate` and whether it is `stale`.
- `PUT /api/v1/strategy-proposals/{id}` with a `body` edits it.
- `POST /api/v1/strategy-proposals/{id}/approve` applies it. A proposal that has
  not passed answers 409 with the code `not_passed`.
- `POST /api/v1/strategy-proposals/{id}/reject` takes an optional `reason`.

### Scenarios (admin)

The **Scenarios** tab lists the benchmark scenarios exported from confirmed
investigations, most recently changed first, and filters them by status. To
make one, an admin opens a confirmed investigation and chooses **Export as
scenario**; the draft opens on this tab. The same export is
`sidekick bench export`; see
[Exporting a real investigation](benchmark.md#exporting-a-real-investigation)
for what the draft holds and how it is cleaned.

A scenario opens with:

- **What to check first:** a reminder of what the cleaner can and cannot
  find, the places it kept because it could not tell what they are, and what the
  exporter skipped or merged.
- **Original and cleaned:** each part of the scenario (the alert payload, what
  each fixture matches on, each answer, the expected cause) next to the recorded
  original it was made from. Values the cleaner replaced, such as `host-1`, and
  the places it kept, are marked; the marks have text or a tooltip, never colour
  alone. The original is read from the investigation's events and steps; once the
  retention job has deleted them, the part says that the original is no longer
  kept.
- **Scenario file:** the whole YAML file, to edit. Saving checks the file and
  refuses one that is not a valid scenario or holds an email, a host name or an
  IP address. A saved edit puts the scenario back to draft.
- **Review:** the box "I checked this for sensitive data" and **Mark as
  reviewed**. The button works only after the tick and with no unsaved edits. A
  reviewed scenario shows who reviewed it and when, and can be downloaded.

## Action proposals

The **Actions** tab of the review queue (`/review?tab=actions`) lists the
actions the model proposed on routes in `approved` mode, newest first, for
the `approver` and `admin` roles. Each proposal shows its action, route,
investigation, when it expires, the parameters whole, exactly as the action
will run with them, and the model's reason, which is the model's own words
and not checked. The parameters are plain text in which characters that
could hide something, such as right-to-left marks and zero-width spaces,
are written as `\u` escapes, and nothing is cut. Approve asks to confirm, as it does in Slack, and then runs the action
once; Reject takes an optional note for the audit log. Of two people who
decide at the same moment, one decides and the other is told who did. A
proposal that expired, or that the config no longer allows because its
route is read-only now or no longer lists the action, answers 409 and can
only be rejected. The investigation page lists its proposals too, with the
same buttons for approvers.

Two menus choose what the tab lists. **Action** is **Any action** or one of
the action names in the `actions` section of the config. **Status** is one of
these, and each asks for the statuses beside it:

| Choice | Address word | Statuses |
|---|---|---|
| Waiting for a decision (the default) | `waiting` | `proposed` |
| Approved (approved and waiting to run, or running) | `approved` | `approved`, `executing` |
| Ran | `ran` | `succeeded` |
| Failed | `failed` | `failed` |
| Expired | `expired` | `expired` |
| Undone | `undone` | `undone` |
| Rejected | `rejected` | `rejected` |
| Any status | `any` | every status |

Both choices are kept in the address, for example
`/review?tab=actions&action=monitor-pr&status=failed`, so a view can be shared.
A choice that is the default is left out of the address, and a value in the
address that the tab does not know is ignored. An empty list names the
filters, for example "No failed actions for monitor-pr". The filter is by
the action's name, which is what a proposal stores, so only names in the
config are offered; proposals of an action that was removed from the config
show under **Any action**.

A proposal that changes a definition in place, such as a monitor, also shows
**Changes, field by field**: each field with its value before and after, in
two labelled blocks that scroll and take focus, as plain text with hidden
characters written as `\u` escapes. A proposal that deletes something says
so, and Approve repeats that Sidekick cannot undo a delete. When the change
worked and the action can undo it, an approver sees **Undo**, which asks
once. Undo puts back only the fields Sidekick changed and leaves every other
field as it is. If someone changed one of those fields since, nothing is
restored: the card shows what a restore would overwrite, and **Restore
anyway** restores it. The undo is a proposal of its own, approved by the
person who asked, and the original becomes undone. The undo shows **Undoes**
with a link to the proposal it restores, which opens its investigation and
moves focus to that proposal.

A proposal that opens a pull request also lists its **Files**: each file
with its path, what happens to it (added, changed or deleted) and how many
lines it adds and removes. The list comes from what Sidekick recorded when
the model proposed, so it needs no call to the host. Each file is a
disclosure, open when the proposal has up to three files and closed when it
has more. When a person opens a file, Sidekick reads the file as it is on the
base branch from the host, with the credential of the action, and compares it
with the content in the proposal's parameters. The file is read when it is
opened and is never stored. The diff has a line number for each side, a
`+` or `-` sign and the words "Added" and "Removed" for a screen reader, as
well as a tint, so color is never the only signal. It shows the changed lines
with three unchanged lines around each change, and says where lines were left
out. A diff longer than 400 lines is cut there, and **Show the whole file**
asks for every line of the file. The lines are plain text with hidden
characters written as `\u` escapes, as in the parameters; this includes a
carriage return at the end of a line from a file with Windows line ends. When
the host cannot give the file, for example because the host no longer has it
or it is not text, the card says so and offers to try again. The diff
answers also for an expired or rejected proposal. Slack keeps the field-by-field
changes and links to the investigation, where the diff is.

A proposal that rolls a workload back to an earlier revision shows
**Revisions** instead of the field-by-field list: two labelled blocks,
**Running now** and **Goes back to**, each with the platform's name for the
revision and its ID, the version a person recognises (an image tag, a chart
and app version or a git revision), when it was deployed, who deployed it and
short notes. The platform does not always say, for example Helm and Lambda do
not record who deployed, and the card then shows "unknown". The texts come
from the platform, so they are plain text with hidden characters written as
`\u` escapes, and nothing is cut. Approve repeats the two versions when it
asks. The undo of a rollback shows the same two revisions the other way round.

A proposal that opened a pull request also shows a **Pull request**
section after its result. It has the repository and number as a link to the
pull request, its state (**Open**, **Merged** or **Closed**, each with an icon
and the word), when it was merged or closed, and when Sidekick last read it
from the Git host. Sidekick reads the state once a day, so what the card
shows can be a day old; a pull request that was merged or closed is not read
again, so its checked time stays at the time the check found it merged or
closed. A proposal with no pull request, or one that was not checked yet,
has no section. The link opens only when the address is an `http` or `https`
one; otherwise the repository and number are plain text.

- `GET /api/v1/proposals` (approver) lists proposals a page at a time, with
  `status` (repeat it for several, default `proposed`), `action` (one name
  from the config; a name that is not there answers 400 and lists the names),
  `cursor` and `limit`.
- `GET /api/v1/proposals/actions` (approver) returns the action names in the
  config, sorted, as `{"actions": [...]}`. It reads the config in memory.
- `POST /api/v1/proposals/{id}/approve` and `/reject` (approver) decide a
  proposal. Only a person who is signed in approves; an API token answers
  403 to an approval, because a script that holds a token could otherwise
  approve every proposal. A token with the approver role can list the queue
  and reject. The 409 answers have a `code`: `expired`, `decided` (the detail
  says who decided) or `not_allowed`.
- `POST /api/v1/proposals/{id}/undo` (approver) undoes a succeeded
  proposal whose `undoable` is true. Like approving, it is for a person who
  is signed in; an API token answers 403. The body is optional:
  `{"force": true}` restores although a field changed since. Without it, a
  changed field answers 409 with `code` `conflict` and `changes`, the
  difference a restore would overwrite. The other 409 codes are
  `not_undoable`, `not_allowed`, `undo_running` and `undo_failed`. A proposal
  has `changes` (field, before and after), `target`, `undoable` and `undoOf`.
  A rollback also has `revisions`, with `running` and `goesBackTo`; each has
  `kind` and `id`, and `version`, `deployedAt`, `deployedBy` and `notes` when
  the platform says them. `changes` is still sent for it.
- `GET /api/v1/proposals/{id}/files/diff?path=...` (approver) returns the
  line diff of one file of a pull request proposal, as a list of lines, each
  `context`, `added` or `removed`, with its line number in the old and the
  new file. It answers for a proposal of any status, so a person can read what
  was proposed after it expired or was rejected. It answers 400 without
  `path`, 404 for a proposal or a path the proposal does not change, 409 with
  `code` `diff_unavailable` when the action cannot read files from its host,
  and 502 when the host did not give the base of the file. A proposal has
  `files`, each with `path`, `status`, `linesAdded`, `linesRemoved` and
  `diffAvailable`.
- `GET /api/v1/investigations/{id}` has a `proposals` list.
- A proposal in the queue and in that list has a `pullRequest` object when a
  check has recorded one: `repo`, `number`, `url`, `state` (`open`, `merged`
  or `closed`), `mergedAt`, `closedAt` and `checkedAt`. The answers to
  approving, rejecting and undoing do not have it.

Every proposal, approval, rejection, expiry and result is in the audit log
as `action.proposed`, `action.approved`, `action.rejected`,
`action.expired`, `action.executing`, `action.succeeded`,
`action.failed`, `action.undo_requested` or `action.undone`. Operators write [the actions](configuration.md#actions) and
developers add types with [Writing an action](plugins/actions.md).

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
  `strategy`, `sources`, `outputs`, `details`, `mode`, `actions`,
  `budgets` and `grouping`. Durations are written as `5m`. A route needs a `match`, uses
  only the sources, outputs, strategies and actions of the config file,
  keeps its budgets at or below the global ones, and has the mode
  `read-only`, `approved` or `read-write`; an `approved` or `read-write`
  route lists at least one action. A `read-write` route runs the model's
  proposals without a person, so it can select only trusted intakes, which
  the runtime listeners and mentions never are, unless the body sets
  `allowUntrustedWrite: true`; see [read + write
  mode](configuration.md#read--write-mode-and-trusted-senders). Only an
  admin can save a route, so only an admin can let the model propose
  actions, and Slack commands cannot. A runtime listener is never trusted.
  A proposal that a `read-write` route approved has `approvedByPolicy: true`
  and no decider; its `decidedAt` is the time of the approval.
- `PUT` takes the `version` of the rule that was read. If the rule has
  changed since, the answer is 409 with the code `stale`.
- `DELETE /{name}` removes a runtime rule. It also removes a runtime rule
  that a rule of the config file shadows, which nothing else can remove.
- A rule that fails the checks answers 422 with the code `validation` and
  an `errors` list, one entry for each problem, naming the `field` of the
  body, such as `budgets.maxToolCalls` or `match.sender.bots[0]`. A rule of
  the config file answers 409 with the code `file_rule`, a name that is
  taken 409 with `exists`, and an unknown name 404 with `not_found`.

## Strategies

The strategies in effect, and the versions of those saved at runtime, are
under `/api/v1/strategies`. A `viewer` can read them; an `admin` can save a
new version. See [`strategies`](configuration.md#strategies) for how
built-in strategies, strategy files and runtime strategies replace each
other.

- `GET /api/v1/strategies` lists every strategy. Each has a `layer`,
  `built-in`, `file` or `runtime`, and a `status`: `active`; `shadowed`, a
  runtime strategy with the name of a strategy file, which wins; or
  `invalid`, a stored version that no longer passes the checks, with the
  reason in `statusReason`. The `version` is a content hash for a built-in
  strategy and a strategy file, and `runtime:N` for a runtime one, as an
  investigation records it. Strategies in effect come first, by name, then
  the runtime ones that are not. Built-in strategies and strategy files are
  `readOnly`.
- `GET /api/v1/strategies/{name}` returns the strategy in effect with its
  YAML in `body`.
- `GET /api/v1/strategies/{name}/versions` lists the saved versions of a
  runtime strategy, newest first and without their YAML, 50 at a time
  (`limit` takes up to 200); pass the answer's `next` as `before` for the
  older ones. `GET /api/v1/strategies/{name}/versions/{version}` returns
  one version with its YAML.
- `POST /api/v1/strategies/{name}/versions` saves the `body`, a YAML text
  in the format of a strategy file, as the next version, with a required
  `reason` of at most 500 characters. It creates the runtime strategy when
  none is stored, also with the name of a built-in strategy or a strategy
  file. `baseVersion` is the version that was read, or 0 when none was
  stored; if the strategy was saved since, the answer is 409 with the code
  `stale`.
- A save that fails the checks answers 422 with the code `validation` and
  an `errors` list: the field `reason`, `body` for a problem with the whole
  text such as a syntax error, or `body.` and the path in the YAML, such as
  `body.match.textPattern`, with the line at the end of the message. A text
  that is the same as the current version answers 422 with the code
  `unchanged`. Every save is written to the audit log in the same
  transaction as the version.

The **Strategies** page (`/strategies`) lists them. The page of a strategy
shows its text. An admin edits a runtime strategy in a text box with a
reason, and sees the problems of a refused save with their path and line.
A built-in strategy has the button **Edit as runtime strategy**, which
starts a runtime strategy of the same name from its text; a strategy file
can only be read. The history lists the saved versions. Any two can be
compared in a diff, which marks added and removed lines with a sign and
words as well as color, and **Use this version as the base** starts the
editor from an older version, which brings its text back as a new version.
An investigation's page links its `runtime:N` version to that version.

## Admin operations

- `GET /api/v1/config` returns the config Sidekick runs with: `file`, the
  config file with its defaults, which only a config change can alter,
  and `runtime`, the listeners and routes changed at runtime, with their
  version and who changed them last. Secrets appear only as references
  to a Secret's name and key, never as values. In case a credential was
  written into the file by mistake, the user and password of every URL
  are removed, and strings the [redaction](configuration.md#redaction)
  rules match are redacted. The [Settings page](#settings) shows it.
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
- `POST /api/v1/jobs/{id}/discard` deletes a dead job, so it cannot run
  again, and answers 204. The discard is written to the audit log as
  `job.discarded`, with the job's kind, its attempts and its last error,
  masked, in the same transaction. A job that is not dead answers 409 and
  stays, and an unknown ID answers 404.
- `POST /api/v1/investigations/{id}/scenario-draft` exports a confirmed
  investigation as a benchmark scenario draft (201, or 200 with the scenario
  it has already). It answers 409 when nobody confirmed the verdict, and 422
  when the alert has no payload to replay. `GET /api/v1/scenarios` lists
  scenarios (`status` takes `draft` or `reviewed`), `GET
  /api/v1/scenarios/{id}` returns one with the notes and the places to
  check, and each part next to its original, and `PUT /api/v1/scenarios/{id}`
  replaces its file. `POST /api/v1/scenarios/{id}/review` needs
  `sensitiveDataChecked: true` and the `revision` that was read, and a
  signed-in user (an API token gets 403). `GET /api/v1/scenarios/{id}/file`
  downloads a reviewed scenario and answers 409 for a draft. A stale
  `revision` answers 409, and a file that is not a valid scenario or still
  holds an email, a host name or an IP address answers 422. See
  [Exporting a real investigation](benchmark.md#exporting-a-real-investigation).

## Settings

The **Settings** item of the navigation (`/settings`) is a page for the
`admin` role, with tabs: **Config** (`tab=config`), **API tokens** (`tab=tokens`),
which is described under [API tokens](#api-tokens), and **Failed jobs**
(`tab=failed-jobs`), described below. The first tab opens when the address names
none. The tab is in the address (`/settings?tab=config`), so a link opens the
page on it. A role below `admin` that opens the address sees a
message that names the role, and the page sends no request.

The **Config** tab shows the config Sidekick runs with, from
`GET /api/v1/config`. It is read-only: change the config in the config file or
the Helm values. Text on the page is plain text, never HTML.

- **File settings.** One card for each top-level key of the config file, in the
  order the API gives them, closed to a one-line summary. Open a card to see its
  values: a nested map is a list of names and values, a list is a bullet list,
  and a long value scrolls in a block that you can reach with the keyboard. The
  page shows keys it does not know, such as those of a plugin, in the same way.
  Defaults that the file leaves out are shown too. A duration is its text, such
  as `10m0s`.
- **Secrets.** A reference to a Secret shows as "Secret `<name>`, key `<key>`".
  Sidekick never holds the value in its config, so there is no value to show and
  no button to copy one. A reference that is empty shows as "No secret set".
- **Added at runtime.** Two cards, Listeners and Routes, list what was added in
  the web UI or by the `/sidekick` commands: the name, whether it is on or off,
  its version, who changed it last and when, and its settings. A link opens the
  [Listeners or Routes page](#listeners-routes-and-the-audit-log-in-the-ui)
  to change them.
- **Filter.** The text field narrows the cards and the values in them to the
  keys and values that contain the text, ignoring case. The text is not a
  pattern. It is kept in the address as `q`, so a view can be shared, and while
  it is set the cards that match are open.
- Strings that the server redacted are shown as they come.

The **Failed jobs** tab lists the background jobs that failed every attempt and
are marked dead, from `GET /api/v1/jobs?status=dead`, newest first. It reads
the list only while the tab is open, and the tab name shows no count. A job
that is not dead is not listed.

- **The table.** Each row shows the kind of the job, its route ("none" when it
  has none), its attempts as "5 of 5", when it was created, when it died, and
  its last error. The input of a job is never shown.
- **Last error.** The text, masked the same way as in the audit log, is shown
  as plain text, never HTML. A long error is cut to its first lines, and a
  **Show all** button, which the keyboard reaches, shows the whole text and
  **Show less** cuts it again.
- **Retry.** Sends `POST /api/v1/jobs/{id}/retry` and removes the row. If the
  job is not dead any more, or another job with the same unique key is
  pending or running, the server's reason shows on the row and the list is read
  again. A reason for a job that has left the list shows above the table.
- **Discard.** Asks in the page, in a row below the job, what the job was and
  says "It will not run again. This cannot be undone." **Discard** then sends
  `POST /api/v1/jobs/{id}/discard`, which deletes the job. The audit log keeps
  the kind, the attempts and the masked last error as `job.discarded`.
- **Load more.** Shows the next 50 jobs. Rows already shown stay where they
  are when a job is retried or discarded.
- Retention also deletes dead jobs on its own once they are old enough, so
  a job that nobody retries or discards does not stay for ever.

## The menu

The left menu shows only the items the signed-in role can use; the
[table of pages](#the-pages-of-the-web-ui) names the role of each. A viewer, a
responder and an approver see Investigations, Dashboard, Review queue,
Lessons, Routes, Strategies, Listeners and Benchmarks; an admin also sees
Audit log and Settings. Someone who opens the address of a page their role
cannot use, for example from a shared link, sees the page title and a
message that names the role it needs; the page sends no request. The menu is
a convenience, not a guard: the server checks the role on every request.

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
  The route form has a Details box, one `key=value` to a line, for the
  [facts the model is told](configuration.md#routes) about the route; the
  routes table lists them. The route form offers the three modes. In `read-write` mode it also
  shows the checkbox for senders that are not trusted, and saving with it
  on first asks you to confirm, naming the route; Cancel sends nothing.
  If someone else
  changed the rule while the form was open, the form says so and offers
  to load the new version.
- **Audit log** (`/audit`, admins) shows the entries newest first, with
  filters for the time, who, the action and the investigation. The
  filters are in the address, so a view can be shared with a link. An
  entry with an old or new value has a **Show change** button.

## The dashboard

`/dashboard` is open to every role from `viewer` up. One row of filters at
the top applies to the whole page: the time range (the last 7, 30, 90 or 365
days, or two dates), the route, the service, the model and the verdict. They
are kept in the address (`range`, `from` and `to` for custom dates, `route`,
`service`, `model` and `verdict`), so a view can be shared; a value the API
would refuse is ignored, so every link opens. Days are UTC days.

- **Key numbers.** Verdict accuracy from feedback, false alarms found,
  the median time to the first finding, and today's spend against the
  daily cap. Each shows its change against the range before it in words,
  such as "Up 4 points".
- **Quality.** Verdict accuracy over time, precision and recall over
  time, the verdict Sidekick gave against the verdict people confirmed,
  precision and recall of each verdict, accuracy by model and strategy,
  and the newest benchmark runs. Choose two runs in the menus **Compare run**
  and **With run** to compare them; the two runs are in the address too (`a` and `b`). The link **Open this
  comparison in Benchmarks** shows once two runs are chosen and opens the
  same two runs on the [compare page](#comparing-two-benchmark-runs). The
  link **All benchmark runs** opens the
  [Benchmarks page](#the-benchmarks-page).

  The precision and recall chart draws one verdict at a time, by day.
  Choose the verdict with the **Verdict** menu on the card; the default
  is False alarm, since a wrong false alarm hides a real problem. The
  choice is in the address as `scoreVerdict`, and an unknown value is
  ignored. It is separate from the verdict filter of the page, which
  selects the verdict Sidekick gave. A day with no figure for the verdict
  is a gap in the lines. The card has a text summary, such as "False
  alarm: precision 96% and recall 88% over the range, from 120 verdicts
  given and 110 confirmed", and a **Show as table** view that adds the
  counts of verdicts given, confirmed and agreed for each day.

  Recall needs the investigations of every verdict. With the verdict
  filter of the page set, the API reads only the investigations where
  Sidekick gave that verdict, so the card draws precision only, for the
  filtered verdict, and says "Recall needs every verdict. Clear the
  verdict filter to see it." The precision and recall by verdict table
  leaves out its Recall column with the same note.
- **Cost.** Spend per day by model with the daily cap as a dashed line and
  a status when today's spend is near the cap or over it, spend today by
  route, spend by route, the cost per investigation, and the tokens in and
  out. The service and verdict filters do not apply to the spend, and the
  page says so.
- **Operations.** Investigations per day by verdict, the time to the
  first finding, the duration, tool calls per investigation, false alarms
  found, noisy alerts fixed, actions proposed, approved and taken, and
  the downtime that watchers measured by route and service.

Every chart has a text summary, a legend, a tooltip and a **Show as table**
button for the same numbers. A chart with nothing to draw says "No data
for this range" and links to the filters. "Noisy alerts fixed" counts the
false alarms that were fixed, in one of three ways: a responder marked the
fix as applied on the investigation page, or the monitor pull request that
Sidekick opened was merged, or the monitor change that Sidekick made through
the tool's API ran and was not undone. A false alarm counts once, however
many of the three apply. Pull requests are read once a day, so a merge can
show on the chart up to a day late, and **Undo** takes a change out of the
count.

## Dashboard numbers and benchmark runs

The dashboard never reads investigations. It reads two rollup tables, which
a job recounts once a minute from the records of the investigations that
changed: `stats_hourly` (investigations by the hour they were created, with
their route, service, model and verdict) and `quality_daily` (the verdict
Sidekick gave against the verdict people confirmed, by the day). A change
shows in the numbers within about a minute. The rollups keep their history
after the retention job deletes the investigations.
`sidekick_rollup_dirty_buckets` shows how many hours and days wait for a
recount (see [Running Sidekick](running.md#metrics)).

These endpoints need the `viewer` role. The stats endpoints take the same
parameters: `from` and `to`, UTC dates with `to` included (the default is
the last 30 days, and at most 400 days), and the filters `route`,
`service`, `model` and `verdict`. Each answer also has the same numbers
for the range of equal length before it, for showing the change.

- `GET /api/v1/stats/quality` returns the accuracy of the verdicts people
  rated, a count for each day, the confusion matrix, precision and recall
  of each verdict, and the accuracy by model and strategy. A rating of
  wrong without a real verdict counts as wrong in the accuracy and is left
  out of precision and recall. The `verdict` filter selects the verdict
  Sidekick gave. Each day also has `scores`, the precision and recall of
  each verdict given or confirmed on that day, worked out from the day's
  ratings in the same way as for the range, with the same filters. A day
  with ratings of wrong without a real verdict only has an empty list. A
  single day has few ratings, so the numbers move a lot from one day to the
  next. With the `verdict` filter set, only investigations where Sidekick
  gave that verdict are read, so recall is too high, for a day as for the
  range.
- `GET /api/v1/stats/cost` returns what model calls cost, by day, model
  and route, with the tokens, from the same table the daily caps read, and
  today's spend against the global cap and each route's. That table has no
  service or verdict, so those two filters do not apply to it and
  `ignoredFilters` names them. The cost per investigation comes from the
  hourly rollup and takes every filter.
- `GET /api/v1/stats/operations` returns, by day or by hour (`interval`,
  hours for a range of at most 7 days), the investigations by verdict, the
  time to the first finding, the duration, tool calls, false positives,
  noisy alerts fixed (see below), and the recoveries, downtime and timed
  out watchers. A median is worked out from counts in
  buckets of growing width, so it is about right, within a third, and not
  exact. The actions of an investigation count in its hour. Proposed is
  every proposal. Approved is the proposals that a person or the policy
  approved: those that are approved, executing, succeeded or undone, and
  those that failed after a decision; not the rejected or expired ones.
  Executed is the proposals that ran and succeeded, including the undone
  ones, which ran before they were undone.

  Noisy alerts fixed counts, in the hour the investigation was created,
  each investigation whose verdict is `false_positive` and that has at
  least one of these: a responder
  [marked the fix as applied](#actions-on-investigations); an action
  proposal that opened a pull request that changes a monitor, whose status
  is `succeeded` and whose pull request is merged; or an action proposal
  that changed a monitor through the tool's API and whose status is
  `succeeded`. An investigation counts once however many apply. Undo gives
  the proposal the status `undone`, which takes it out of the count, and a
  pull request that was closed without a merge does not count. Mutes,
  rollbacks, flags, tickets and code changes never count. The state of a
  pull request is read once a day (see
  [the pull request check](configuration.md#actions)), so a merge shows up
  to a day late, and a recount of the hour then counts it.
- `GET /api/v1/stats/filters` lists the routes, services and models of the
  last 400 days, for the filter menus.
- `GET /api/v1/bench/runs` lists the benchmark runs that
  `sidekick bench run --db` stored, newest first, a page at a time, with
  their settings and metrics. `GET /api/v1/bench/runs/{id}` adds the
  confidence intervals, the thresholds and the result of each run of each
  scenario; the judge's reasons are left out. `GET /api/v1/bench/compare?a=<id>&b=<id>`
  lines up two runs: each metric with the change and whether the intervals
  overlap (`clear` or `none`), and the correct runs of each scenario.

### The Benchmarks page

`/benchmarks`, in the menu as Benchmarks, is open to every role from
`viewer` up. It lists every run that `sidekick bench run --db` stored, newest
first, 25 at a time; **Load more** adds the next 25. Each row has:

- the run, as its label or its start time, with the Sidekick version and the
  models under it; the name links to `/benchmarks/<id>`
- the result, Passed or Failed, as a word with an icon
- the overall score, the verdict accuracy, the mean cost per run and the mean
  time per run
- the scenario set, with its repeats and the number of runs
- who started the run: Command line (`cli`), CI (`ci`) or Strategy gate
  (`gate`)

The dashboard card shows the same table for the newest ten runs. With no
runs stored, the page says so and names the command that stores them. With
two runs or more on the page, a button **Compare two runs** above the table
opens the [compare page](#comparing-two-benchmark-runs).

### The page of one benchmark run

`/benchmarks/<id>`, which the name of a run in the list links to, is open to
every role from `viewer` up. An ID that no run has shows "No benchmark run has
this ID." with a link back to the list. It shows, from the top:

- **The run.** Its label, or its start time when it has no label, with the
  result Passed or Failed as a word with an icon. A button **Compare with
  another run** opens `/benchmarks/compare?a=<id>`. Below it: when the run
  started and finished, the Sidekick version and Git commit, who started it,
  the scenario set and repeats, the model and connector of each role, the
  version of each strategy the scenarios used, the prompt and judge prompt
  versions, and the config hash, with a button that copies it.
- **The first failed threshold.** For a failed run, a message above the
  tables names the first threshold that failed, in the order the run checked
  them, with its value and its limit, and says how many others also failed.
- **Scores.** Each share metric of [Scores](benchmark.md#scores): overall
  score, verdict accuracy, false positive and true positive precision and
  recall, root cause match and score, evidence, proposals correct and source
  failures reported. Each has its value and its 95% interval, written
  "62% to 88%". A metric the run had nothing to count shows "none yet", and a
  metric that has no interval shows "none". Two runs whose intervals of a
  metric overlap may differ only by chance.
- **Thresholds.** The thresholds of `bench.yaml` that the run was checked
  against: the limit ("at least 80%", or "at most $0.0500" for the mean cost
  of a run), the run's value, Passed or Failed as a word with an icon, and the
  detail the check gave, for example that a metric with nothing to count
  passes. Percentages here show one decimal, so a miss never looks equal to
  its limit.
- **Cost and time.** The total model cost, the judge's own cost, input and
  output tokens, tool calls, the mean cost and time of a run, and the number
  of judge errors. A run with many judge errors has a low root cause score
  for that reason, so read this figure before you trust the score.
- **Scenarios.** One row for each scenario, wrong ones first: a scenario with
  no correct repeat is marked **Wrong**, one with some correct and some not
  **Partly wrong**, and one with every repeat correct **Correct**. A row shows
  the expected verdict, how many repeats were correct ("4 of 5"), the verdicts
  given, and the mean root cause score, cost and time. A row opens to its
  repeats: the verdict given, whether it was correct, the root cause score,
  whether every required item of the rubric was met, the evidence score,
  whether the proposals were correct (when the scenario scores them), the
  cost, time, tokens and tool calls. The judge's reasons are not shown, since
  the API leaves them out.

Everything that came from the run, such as labels, scenario IDs and details,
is shown as plain text.

### Comparing two benchmark runs

`/benchmarks/compare`, open to every role from `viewer` up, shows any two
runs side by side. The two runs are in the address as `a` and `b`, the same
names the dashboard uses, so a comparison can be shared with a link; a value
that is not a run ID is ignored, so every link opens. Choosing a run in a
menu, or pressing **Swap runs**, changes the address. The button **Compare
with another run** on the page of a run opens this page with that run as `a`.

- **Choosing.** Two menus, **Run A** and **Run B**, list the newest 50 runs.
  A run in the address that is not among them still shows, named by its ID.
  With no run chosen the page says "Choose two runs to compare them."; with
  one chosen it says which menu is still open. When no run is stored at all,
  it says so. When the comparison fails, for example because no run has an
  ID, the page shows the message of the API.
- **Settings.** A table with one column for each run and a column **Differs**:
  the label, the start, the Sidekick version, the Git commit, the scenario
  set, the repeats, the model of each role, the version of each strategy,
  and the prompt version, judge prompt version and config hash. A row where
  the two runs differ says **Differs**, in a tinted row, and a row where they
  match says **Same**. A role or a strategy that only one run has shows "not
  used" for the other. The start is never compared, since it always differs.
  The prompt versions and config hash come from the page of each run; while
  one is loading the row says "Loading" and "Not compared yet", and if it
  fails the row says "Not available" and a button **Try again** loads it
  again. The rest of the page does not wait for it.
- **Scores.** Each metric of both runs, the change from A to B and whether
  the difference is clear, as on the dashboard card. A difference is clear
  when the confidence intervals of the two runs do not overlap. The mean cost
  and time keep their units, and have no interval, so they say "Not compared".
- **Scenarios.** One row for each scenario of either run, with the expected
  verdict, the correct runs of A and of B ("2 of 2") and the mean root cause
  score of each. A scenario that went from every repeat right to none right
  comes first and says **Right to wrong**; one that went from none right to
  every repeat right comes next and says **Wrong to right**. The rest follow
  by scenario ID and say **Same result**, **Changed in part** (some repeats
  changed), or **Only in Run A** or **Only in Run B**.

The dashboard card shows the same Scores and Scenarios tables.

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
| 409 | The change conflicts with the current state, such as retrying a job that is not dead, changing a rule of the config file, or saving a strategy that was saved again since it was read. |
| 422 | A body that is valid but names something that is not there, such as a finding the report does not have, a listener or route that fails the config checks, or a strategy that fails the strategy checks (the answer lists the fields). |
| 404, 405 | No operation has this path, or this method on it. |
