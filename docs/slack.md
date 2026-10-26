# Investigations in Slack

The `slack` output shows each investigation as one Slack message that is
edited in place while the investigation runs. This release posts with a
bot token through the Slack Web API only; buttons, follow-up questions in
the thread and a link to the web UI come in later releases.

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
  checked. The bar takes the verdict's color, and the verdict is always
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
- **A run that could not finish** shows "Could not finish" with a yellow
  bar and what was checked, and gets a short reply too, so no message is
  left on "Investigating".

Text from the alert and from the model is escaped, so it cannot mention
`@channel` or a person, or add links. The only links are to evidence that
the sources returned.

## Set up the Slack app

1. Create a Slack app for your workspace and add the bot token scope
   `chat:write`. To post in public channels the app has not been invited
   to, also add `chat:write.public`; otherwise invite the app to each
   channel it posts in.
2. Install the app and copy its bot token (`xoxb-...`).
3. Put the token in a Kubernetes Secret, for example `sidekick-slack` with
   the key `botToken`, and point `slack.botTokenSecret` at it.
4. Add a `slack` output and list it in the routes that should post:

```yaml
slack:
  botTokenSecret: { name: sidekick-slack, key: botToken }

outputs:
  slack-thread: { type: slack, channel: "#alerts" }

routes:
  - name: default
    outputs: [slack-thread]
```

Sidekick reads the token each time it calls Slack, so a rotated token is
used without a restart.

## Keys

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
