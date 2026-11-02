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
- **The investigation page** (`/investigations/<id>`, the address the
  Slack link opens) shows the verdict, how sure Sidekick is, the reason,
  the state of the watchers, the report, what was checked, the cost and
  the model, the lessons the prompt used, the alerts of the group, and the
  record of the run, one line for each step. A step opens to show the tool
  call or result in a box with a copy button. The prompt step, and the
  triage prompt step, also say how many lessons they used and which
  lessons applied but were left out, with the reason; see
  [Lessons](#lessons).

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
move between the tabs.

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
for what turns them on and what they cost. Only an `admin` sees them.

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
the `approver` and `admin` roles. It shows the proposals that wait for a
decision, or those already decided. Each proposal shows its action, route,
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

A proposal that changes a definition in place, such as a monitor, also
shows **Changes, field by field**: each field with its value before and
after, as plain text with hidden characters written as `\u` escapes. A
proposal that deletes something says so, and Approve repeats that Sidekick
cannot undo a delete. When the change worked and the action can undo it, an
approver sees **Undo**, which asks once. Undo puts back only the fields
Sidekick changed and leaves every other field as it is. If someone changed
one of those fields since, nothing is restored: the card shows what a
restore would overwrite, and **Restore anyway** restores it. The undo is a
proposal of its own, approved by the person who asked, and the original
becomes undone.

- `GET /api/v1/proposals` (approver) lists proposals a page at a time, with
  `status` (repeat it for several, default `proposed`), `cursor` and
  `limit`.
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
- `GET /api/v1/investigations/{id}` has a `proposals` list.

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
- **Quality.** Verdict accuracy over time, the verdict Sidekick gave
  against the verdict people confirmed, precision and recall of each
  verdict, accuracy by model and strategy, and the newest benchmark runs.
  Choose two runs to compare them; the two runs are in the address too
  (`a` and `b`).
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
for this range" and links to the filters. A responder marks the fix of a
false positive as applied on the investigation page; that is what "noisy
alerts fixed" counts.

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
  Sidekick gave.
- `GET /api/v1/stats/cost` returns what model calls cost, by day, model
  and route, with the tokens, from the same table the daily caps read, and
  today's spend against the global cap and each route's. That table has no
  service or verdict, so those two filters do not apply to it and
  `ignoredFilters` names them. The cost per investigation comes from the
  hourly rollup and takes every filter.
- `GET /api/v1/stats/operations` returns, by day or by hour (`interval`,
  hours for a range of at most 7 days), the investigations by verdict, the
  time to the first finding, the duration, tool calls, false positives,
  noisy alerts fixed (false positives whose fix a responder
  [marked as applied](#actions-on-investigations)), and the recoveries,
  downtime and timed out watchers. A median is worked out from counts in
  buckets of growing width, so it is about right, within a third, and not
  exact. The actions of an investigation count in its hour. Proposed is
  every proposal. Approved is the proposals that a person or the policy
  approved: those that are approved, executing, succeeded or undone, and
  those that failed after a decision; not the rejected or expired ones.
  Executed is the proposals that ran and succeeded, including the undone
  ones, which ran before they were undone.
- `GET /api/v1/stats/filters` lists the routes, services and models of the
  last 400 days, for the filter menus.
- `GET /api/v1/bench/runs` lists the benchmark runs that
  `sidekick bench run --db` stored, newest first, a page at a time, with
  their settings and metrics. `GET /api/v1/bench/runs/{id}` adds the
  confidence intervals, the thresholds and the result of each run of each
  scenario; the judge's reasons are left out. `GET /api/v1/bench/compare?a=<id>&b=<id>`
  lines up two runs: each metric with the change and whether the intervals
  overlap (`clear` or `none`), and the correct runs of each scenario.

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
