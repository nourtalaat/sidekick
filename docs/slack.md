# Investigations in Slack

Sidekick works in Slack in two directions. The Slack app receives
messages: alerts that bots post in the channels it listens to, and
mentions of `@Sidekick`, each of which starts an investigation, and
follow-up questions in the thread of an investigation. The `slack` output
shows each investigation as one Slack message that is edited in place
while the investigation runs, with buttons to stop the run and to rate
its verdict, and answers the questions in its thread. Slash commands and
a link to the web UI come in later releases.

## What it posts

- **When the investigation is created**, it posts "Looking into it" with
  the alert's title, within seconds of the alert. For an alert that came in
  through a webhook, the message goes in the output's `channel`. For an
  alert posted in Slack, it goes in that message's thread.
- **While the investigation runs**, it edits the message with the steps
  done so far, at most once every 3 seconds. The message's bar is orange.
- **When the investigation finishes**, it edits the message to show the
  verdict, the confidence, the reason, the likely cause and impact (for a
  false alarm, why it fired and how to stop it), up to 5 findings with
  links to their evidence, up to 3 proposed fixes, and which sources were
  checked. A finding with no evidence is marked as a hypothesis, and an
  investigation that stopped at a limit says so. The bar takes the verdict's color, and the verdict is always
  shown with its emoji and label too, so the color is never the only sign:

  | Verdict | Emoji | Bar |
  |---|---|---|
  | Real issue | `:rotating_light:` | red |
  | False alarm | `:no_bell:` | green |
  | Not sure yet | `:grey_question:` | gray |
  | Duplicate | `:link:` | blue |
  | Known issue | `:pushpin:` | blue |
  | Flapping | `:repeat:` | teal |

  A longer report continues in a reply in the thread.
- **Every final verdict also gets a short new reply** in the thread, with a
  link to the full report, because Slack does not notify anyone when a
  message is edited. This happens for every investigation, including "Not
  sure yet".
- **A triage-only result**, when [triage](investigations.md#triage)
  found that no full investigation is needed, shows the triage's verdict
  (Duplicate, Known issue or Flapping) with "Triage only · No full
  investigation ran".
- **A run that could not finish** shows "Could not finish" with a yellow
  bar and what was checked, and gets a short reply too, so no message is
  left on "Investigating".
- **Each follow-up answer** is a new reply in the thread, which mentions
  the people who asked. An answer that changes the verdict also updates
  the message and gets a new verdict reply. See
  [Follow-up questions](#follow-up-questions).

Text from the alert and from the model is escaped, so it cannot mention
`@channel` or a person, or add links. The only links are to evidence that
the sources returned.

## Buttons

- **Stop** is on the message while the investigation is queued or
  running. Slack asks to confirm first. The run stops within a few
  seconds, even in the middle of a slow model or tool call and whichever
  replica runs it, and the message then shows "Not sure yet" with what was
  found and checked so far, and who stopped it.
- **Correct** and **Wrong** are on the final message, except after a Stop
  or a run that could not finish. Each person has one rating per verdict:
  clicking the same button again changes nothing, and clicking the other
  one changes the rating. The message then says who marked the verdict
  correct or wrong.

- **Investigate anyway** is on a triage-only result, next to Correct and
  Wrong. It starts the full investigation, with the strategy triage chose,
  and the same message then shows its progress and its verdict. It works
  once: a second click, or one on any other result, changes nothing.

A Correct confirms the verdict, which marks the investigation as one whose
verdict people agree with. A later Wrong on a confirmed verdict takes the
confirmation back. Investigate anyway clears the confirmation of the triage
verdict, since the full investigation gives a verdict of its own; the
ratings of the triage verdict are kept.

Every click is checked against the clicker's role at the time of the
click: **Stop**, **Correct**, **Wrong** and **Investigate anyway** need
the `responder` role, so a `viewer` cannot press them. Roles come from
`slack.roleGroups` and `slack.roleUsers`, and a Slack user in neither is a
`responder`; see [Roles](#roles). A refused user's groups are read again
before the click is refused, so someone just added to a group can press at
once. When a click cannot do anything, for example a Correct before there
is a verdict, or a Stop on an investigation that has ended, Sidekick says
so in a note only the clicker sees.

Ratings are counted in `sidekick_feedback_total`, by target (`verdict`)
and rating (`correct` or `wrong`); a repeated click is not counted.

## Set up the Slack app

Each organization creates its own internal Slack app from the manifest
in [`deploy/slack/manifest.yaml`](../deploy/slack/manifest.yaml); there is
no shared Sidekick app to install.

1. In [api.slack.com/apps](https://api.slack.com/apps), choose **Create
   New App**, then **From a manifest**, pick your workspace and paste the
   manifest.
2. Install the app to the workspace and copy its bot token (`xoxb-...`).
3. Under **Basic Information**, create an app-level token with the
   `connections:write` scope (`xapp-...`). Socket Mode uses it, so
   Sidekick needs no public URL.
4. Put both tokens in a Kubernetes Secret, for example:

   ```sh
   kubectl create secret generic sidekick-slack \
     --from-literal=botToken=xoxb-... --from-literal=appToken=xapp-...
   ```

5. Point the `slack` section at them, add a `slack` output and list it in
   the routes that should post:

```yaml
slack:
  botTokenSecret: { name: sidekick-slack, key: botToken }
  appTokenSecret: { name: sidekick-slack, key: appToken }

listeners:
  alerts:
    channel: "#alerts"
    match:
      sender: { kind: bot }

outputs:
  slack-thread: { type: slack, channel: "#alerts" }

routes:
  - name: default
    outputs: [slack-thread]
```

6. Invite `@Sidekick` to each channel it should listen to or post in.

Sidekick reads the bot token each time it calls Slack, and the app token
each time it opens a connection, so a rotated token is used without a
restart. A `slack` section with only `botTokenSecret` still works: Sidekick
then posts to Slack but receives nothing, and logs that at start-up.

## Receiving messages

With `appTokenSecret` set, each replica opens one Socket Mode connection to
Slack. Slack allows 10 connections per app, so with more than 10 replicas
only 10 connect; the others take over when one of them stops. Every
message is stored before Slack is told it arrived, keyed by Slack's event
ID, so a message that Slack sends again is stored once, and a message
that could not be stored is sent again by Slack.

Teams that prefer the Events API set `mode: events` and the app's signing
secret (under **Basic Information**, **App Credentials**) in
`signingSecret`, and turn Socket Mode off in the app's settings. Slack then
sends events to `POST /slack/events` on `server.listen`, which must be
reachable from Slack, and enter `https://<your host>/slack/events` as the
**Request URL** under **Event Subscriptions**. Each request is checked
with the signing secret, and one more than 5 minutes old is refused.

```yaml
slack:
  botTokenSecret: { name: sidekick-slack, key: botToken }
  signingSecret: { name: sidekick-slack, key: signingSecret }
  mode: events
```

In events mode, button clicks go to `POST /slack/interactivity`, checked
with the signing secret in the same way: enter
`https://<your host>/slack/interactivity` as the **Request URL** under
**Interactivity & Shortcuts**. Over Socket Mode, clicks arrive on the same
connection as events.

Slash commands go to `POST /slack/commands` in events mode: enter
`https://<your host>/slack/commands` as the **Request URL** of the
`/sidekick` command under **Slash Commands**. Command requests are
checked with the signing secret in the same way.

## Listeners and mentions

### Listeners

A [listener](configuration.md#listeners) picks the messages in one
channel that start an investigation, usually alerts that a monitoring
bot posts. For each new message in the channel:

- Edits, deletions, joins, replies in threads and Sidekick's own messages
  are skipped. A message that mentions `@Sidekick` is handled as a
  mention instead.
- The text Sidekick sees is the message text with the text of its
  attachments (pretext, title, text, fields and footer) and its blocks
  added, one part per line. `match.text`, `extract` and `recovery` all run
  on it.
- When several listeners match one message, the first one in name order
  takes it, so each message starts at most one investigation.
- `extract` patterns set fields from named groups: `alert` sets the alert
  name, `service` the service, `severity` the severity (names such as
  `critical`, `warning` or `info`), and any other name a label. The first
  pattern that sets a field wins.
- The alert name and labels make the event's fingerprint, which groups
  repeated alerts. Set an `alert` group, so that an alert and its
  recovery message have the same fingerprint. A message that gave no
  alert name and no labels gets a fingerprint of its own.
- A message that `recovery` matches becomes a recovery event for the same
  fingerprint, instead of a new alert.
- A bot's message is an alert and a person's message is a plain message.
  Events are trusted only from a listener with `trusted: true`, and only
  when the bot is in `slack.trustedBots`; everything else is untrusted.

The listener's name is the event's intake, so routes can pick listener
events with `match.intake`.

### Mentions

Mentioning `@Sidekick` asks it to investigate a message:

- In a reply in a thread, it investigates the thread's first message,
  which it reads with `conversations.replies`. When a listener would take
  that message, its `extract` patterns and fingerprint apply too, so the
  mention joins the alert's investigation once grouping is available.
- In a top-level message, it investigates the message's own text, without
  the mention.

Events from mentions have the intake `slack-mention` and are never
trusted. A mention in a thread that Sidekick started itself is left alone,
and a mention in the thread of an investigation is a follow-up question.

### Follow-up questions

A reply in the thread of an investigation's message is a follow-up
question: with `slack.followUps: mention`, the default, only a reply that
mentions `@Sidekick`, and with `followUps: any`, every reply a person
writes there. Replies from bots never are. Sidekick finds the
investigation from the thread, so the question goes to that investigation
rather than starting a new one.

Sidekick answers in a new run of the same investigation, which starts from
everything the first run saw:

- Questions that arrive close together, or while the investigation still
  runs, are answered together in one run, after the investigation's own
  verdict.
- The run has the same tools as the first, and half of its limits: half the
  tokens, tool calls, time and cost. See [Limits](investigations.md#limits).
- When the answer does not change the verdict, it is posted as a reply.
  When it does, the model gives a new report, which replaces the old one,
  and the message shows the new verdict. The report keeps every earlier
  answer either way.
- A run that cannot answer, for example at its limits or because someone
  stopped it, posts a reply that says so, and the report stays as it was.
- An investigation that was stopped still answers questions; the Stop
  applies only to the run it was pressed for.

A question is redacted like event text before it is stored, and the model
reads it as a request about what to look at: it cannot change the
investigation's tools, its action mode or the verdict rules.

Asking needs the `responder` role, like the buttons (see [Roles](#roles)).
A `viewer`'s question is not sent: Sidekick starts no run for it and
replies in the thread with a note only that person sees, "Only responders
can ask Sidekick follow-up questions, so this one was not sent." As with
the buttons, a refused user's groups are read again first, so someone just
added to a group can ask at once.

## The /sidekick command

`/sidekick` lets people add listeners and routes from Slack, without a
config change, and ask Sidekick to investigate any message. Replies are
shown only to the person who sent the command.

| Command | Role | Does |
|---|---|---|
| `/sidekick listen #channel [bot:<name or ID>] [pattern:<regex>] [name:<name>]` | responder | Adds a listener for the channel. `bot:` takes a bot name such as `Grafana` or a bot ID such as `B01ABCDEF`, and can be given more than once; `pattern:` is a regular expression over the message text, in double quotes when it holds spaces. The listener is named after the channel unless `name:` is given. |
| `/sidekick unlisten <name>` | responder | Removes a listener added with `listen`. |
| `/sidekick listeners` | viewer | Lists the listeners in effect. |
| `/sidekick routes` | viewer | Lists the routes, in the order they are tried. |
| `/sidekick route set <name> <field> <value>` | admin | Changes one field of a route added from Slack, or adds the route. `/sidekick route set` lists the fields. |
| `/sidekick investigate <message link>` | responder | Investigates the message, as a mention of `@Sidekick` in its thread would. Use the message's **Copy link**; for a reply in a thread, the thread's first message is investigated. |
| `/sidekick status` | anyone | Shows the version, your role and how many rules are in effect. |
| `/sidekick help` | anyone | Lists the commands. |

`route set` can change these fields: `priority`, `enabled`, `mode`,
`strategy`, `sources`, `outputs`, `intake`, `service`, `severity`,
`labels`, `text`, `maxToolCalls`, `maxTokens`, `maxCostUSD`,
`maxWallTime`, `window` and `relatedWindow`. List fields
take values separated by commas, and `none` clears a list or a match
field. A new route needs a match field (`intake`, `service`, `severity`,
`labels` or `text`) first, so it never takes every event ahead of the
`default` route. Routes set from Slack are `read-only`.

### Rules from Slack and from the config file

Listeners and routes added from Slack are kept in Postgres, so every
replica uses them, and each replica reloads them within a second of a
change and again every minute. They pass the same checks as those in the
config file, and may use only what the file defines: its sources,
outputs and strategies, with budgets at or below
`budgets.perInvestigation`. Infrastructure, such as connectors, sources,
outputs and credentials, is set only in the file.

- The config file wins: a rule from Slack cannot take the name of a file
  rule. If a config change later adds a file rule with the name of one
  from Slack, the file rule is used, and the other is logged at start-up
  and shown as not in effect by `/sidekick listeners` and
  `/sidekick routes`.
- Routes are tried by `priority`, lower first, then routes from the file
  before those from Slack, then by name. The `default` route is set only
  in the file and is always tried last.
- A rule from Slack that a config change makes invalid, for example by
  removing a source it uses, stops being used and is logged.
- Every change is written to the audit log with who made it and the old
  and new values.

### Roles

Until web UI login arrives, roles come from the `slack` section:
`roleGroups` maps Slack user groups to roles, and `roleUsers` maps single
users, for teams without user groups. A user gets the highest role from
either; anyone else is a `responder`. Each role can do everything the
roles below it can: `viewer`, then `responder`, then `approver`, then
`admin`.

```yaml
slack:
  roleGroups:
    S01SRE: admin          # the user group ID, from the group's page in Slack
  roleUsers:
    U01LEAD: approver
```

Sidekick reads a user's groups with `usergroups.users.list`, one call per
group in `roleGroups`, and keeps them for a day. When someone is refused
for their role, it reads their groups again at once, so a person just
added to a group does not wait.

## Output keys

| Key | Required | Meaning |
|---|---|---|
| `channel` | yes | Where messages for webhook alerts go: a channel name such as `#alerts`, or a channel ID. |
| `apiURL` | no | The Slack Web API base URL, ending in `/`. The default is `https://slack.com/api/`; GovSlack workspaces use `https://slack-gov.com/api/`. |

## Limits and errors

Sidekick waits at least 3 seconds between edits of one message, limits its
calls in each channel on each replica, and waits for Slack's `Retry-After`
when Slack answers `429`. A call that fails is retried with backoff for
about half an hour. Failed calls are counted in
`sidekick_slack_api_errors_total`, by Web API method and error, such as
`channel_not_found` or `not_in_channel`; see
[Running Sidekick](running.md#metrics).

Received Slack events count in `sidekick_events_received_total` with the
listener's name, or `slack-mention`, as the intake. Events API requests
turned away before their event is read, for a wrong signature or an old
timestamp, count in `sidekick_events_rejected_total` with the intake
`_slack`.
