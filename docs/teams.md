# Investigations in Microsoft Teams

Sidekick receives Microsoft Teams messages through a Teams bot: alerts
that bots post in the channels it is in, and mentions of `@Sidekick`, each
of which starts an investigation. Posting results back in Teams threads
comes in a later release.

The bot is your organization's own Azure Bot, which the Bot Framework
connects to Sidekick's messaging endpoint, `POST /teams/messages`. That
endpoint must be reachable from the internet over https; see
[Reaching the messaging endpoint](#reaching-the-messaging-endpoint).

## Set up the bot

You need rights to create app registrations or managed identities in
Microsoft Entra ID, an Azure subscription for the Azure Bot resource, and
a Teams admin who allows custom apps.

1. **Make the bot's identity.** Either:
   - register a single-tenant app in Microsoft Entra ID (**App
     registrations**, **New registration**, "Accounts in this
     organizational directory only"), and give it a client secret or, on
     AKS, a federated credential; or
   - make a user-assigned managed identity with a federated credential.

   The federated credential for AKS workload identity trusts the cluster's
   OIDC issuer for Sidekick's service account:

   ```sh
   az identity federated-credential create --name sidekick \
     --identity-name sidekick-bot --resource-group <group> \
     --issuer "$(az aks show -g <group> -n <cluster> --query oidcIssuerProfile.issuerUrl -o tsv)" \
     --subject system:serviceaccount:sidekick:sidekick \
     --audiences api://AzureADTokenExchange
   ```

   For an app registration, add the same credential under **Certificates
   & secrets**, **Federated credentials**. [Cloud
   credentials](install.md#cloud-credentials) shows how the chart labels
   the pods for workload identity. The identity's client ID is the bot's
   app ID.
2. **Create the Azure Bot.** In the Azure portal, create an **Azure Bot**
   with the type **Single Tenant** or **User-Assigned Managed Identity**
   and the identity from step 1. Microsoft no longer creates multi-tenant
   bots. Under **Configuration**, set the **Messaging endpoint** to
   `https://<your host>/teams/messages`. Under **Channels**, add
   **Microsoft Teams**.
3. **Make the Teams app.** Copy
   [`deploy/teams/`](../deploy/teams/), put the bot's app ID in place of
   every `00000000-0000-0000-0000-000000000000` in `manifest.json` (as the
   app's `id`, the bot's `botId` and `webApplicationInfo.id`), fill in
   `developer`, and zip `manifest.json`, `color.png` and `outline.png`
   together, with no folder inside the zip.
4. **Install it.** Upload the zip in the Teams admin center (**Teams
   apps**, **Manage apps**, **Upload new app**) or in Teams with **Upload a
   custom app**, then add it to each team whose channels Sidekick should
   watch. The team's owner is asked to grant the app
   `ChannelMessage.Read.Group`; see [Receiving
   messages](#receiving-messages).
5. **Configure Sidekick** with the bot's identity:

   ```yaml
   teams:
     appID: 00000000-1111-2222-3333-444444444444      # the bot's app ID
     tenantID: 55555555-6666-7777-8888-999999999999
     workloadIdentity: true                           # or clientSecret: { name: sidekick-teams, key: clientSecret }
     trustedBots: ["28:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]
   ```

   [Configuration](configuration.md#teams) describes every key.

## Reaching the messaging endpoint

The Bot Framework posts every activity to `/teams/messages` on
`server.listen`, the same port as webhooks, so it must be reachable at a
public https URL. Teams has nothing like Slack's Socket Mode.

The chart adds no Ingress for it by default. With `ingress.enabled`, add
the path next to `/hooks/`:

```yaml
ingress:
  enabled: true
  hosts:
    - host: sidekick.example.com
      paths:
        - { path: /hooks/, pathType: Prefix }
        - { path: /teams/messages, pathType: Exact }
  tls:
    - { hosts: [sidekick.example.com], secretName: sidekick-tls }
```

`httpRoute` takes one `pathPrefix`, so for both paths add a second
HTTPRoute of your own for `/teams/messages`. Keep the metrics port off the
public network.

With `networkPolicy.httpFrom` set, allow the ingress controller to reach
the port. With `networkPolicy.restrictEgress`, allow
`login.botframework.com`, `login.microsoftonline.com` and
`graph.microsoft.com` on port 443.

## Receiving messages

By default Teams sends a bot only the messages that mention it. The app
manifest asks for the resource-specific consent permission
`ChannelMessage.Read.Group`, which lets the bot receive every message in
the channels of a team it is installed in. The team's owner grants it
when adding the app. Without it, only mentions arrive and listeners see
nothing.

Every activity is checked before anything reads it:

- Its token must be signed by one of the Bot Framework's published keys,
  issued by `https://api.botframework.com` for the bot's app ID, unexpired
  (with 5 minutes of leeway for clocks), and for the activity's service
  URL, with a key endorsed for Teams. The keys are fetched again every 24
  hours, and early when a token names a key Sidekick does not have, but at
  most once every 5 minutes.
- It must be for this bot and from `teams.tenantID`. An activity from any
  other tenant is refused before anything is stored.

Each message is stored before the Bot Framework is answered, keyed by its
channel and message ID, so a message sent again is stored once.

Message text is untrusted. It is redacted before it is stored and never
written to the logs.

## Listeners and mentions

### Listeners

A [listener](configuration.md#listeners) with `source: teams` picks the
messages in one Teams channel that start an investigation. It works like
a [Slack listener](slack.md#listeners), with Teams IDs:

- `channel` is the channel's ID, such as `19:abc123@thread.tacv2`. In
  Teams, **Get link to channel** gives a link that holds it, URL-encoded,
  after `/channel/`.
- `sender.bots` are Teams bot IDs: `28:` and the bot's app ID.
  `sender.names` are bot names. `sender.users` are people's Microsoft
  Entra object IDs.
- Edits, deletions, replies in threads, chats outside channels and
  Sidekick's own messages are skipped. A message that mentions Sidekick
  is handled as a mention instead.
- The text Sidekick sees is the message as plain text, with the text of
  its cards (Adaptive Cards, hero and thumbnail cards, and Office 365
  connector cards) added, one part per line. Links in the message and the
  cards' buttons are kept as the event's links.
- Events are trusted only from a listener with `trusted: true`, and only
  when the bot is in `teams.trustedBots`; everything else is untrusted.

### Mentions

Mentioning `@Sidekick` asks it to investigate a message:

- In a reply in a thread, it investigates the thread's first message.
  When a listener already stored that message, Sidekick uses the stored
  event and makes no call. Otherwise it reads the message through
  Microsoft Graph (`GET /teams/{team}/channels/{channel}/messages/{id}`)
  with the bot's identity, which `ChannelMessage.Read.Group` allows. A
  listener that would take that message applies its `extract` patterns
  and fingerprint too.
- When Graph may not read the message, for example because the team's
  owner did not grant `ChannelMessage.Read.Group`, Sidekick investigates
  the mention's own text instead and logs a warning.
- In a top-level message, it investigates the message's own text, without
  the mention.

Events from mentions have the intake `teams-mention` and are never
trusted. A mention in a thread that Sidekick started itself is left
alone.

## Not known yet

These have not been checked on a real Teams tenant yet:

- **Messages from other bots, incoming webhooks and Workflows.** Many
  alerting tools post to Teams through a bot, an incoming webhook or a
  Workflows (Power Automate) flow. Whether the Bot Framework sends those
  messages to another bot, even with `ChannelMessage.Read.Group`, is not
  documented and not yet confirmed. If they do not arrive, alerts posted
  that way do not reach listeners. Mentioning `@Sidekick` in their thread
  should still work, since the first message is then read through Graph,
  but that is not confirmed either.
- **Private and shared channels.** Only standard channels have been
  designed for.
- **Government clouds.** Only the public cloud's Bot Framework and
  Microsoft Entra endpoints are used.

## Limits and errors

The endpoint answers:

| Status | When |
|---|---|
| 200 | The event is stored, was stored before, or the activity is one Sidekick ignores. |
| 400 | The body is not an activity. |
| 401 | The token is missing or fails a check. |
| 403 | The activity is from another tenant, for another bot, or not from Teams. |
| 413 | The body is larger than 1 MiB. |
| 500 | The event could not be stored, the thread's first message could not be read, or the Bot Framework's keys could not be fetched. |

Received Teams events count in `sidekick_events_received_total` with the
listener's name, or `teams-mention`, as the intake. Activities turned away
before their event is read count in `sidekick_events_rejected_total` with
the intake `_teams`.
