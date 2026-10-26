# Results on tickets

The ticket outputs post the result of an investigation as a comment on the
ticket the alert is about. `linear.comment` comments on a Linear issue and
`jira.comment` on a Jira Cloud issue.

## What they post

- **Only the final result.** Nothing is posted while the investigation
  runs: people read a ticket later rather than watch it, and every new
  comment sends the ticket's watchers a notification.
- **One comment per investigation.** It shows the verdict with its emoji
  and label, the confidence, the reason, the likely cause and impact (for a
  false alarm, why it fired and how to stop it), up to 8 findings with
  links to their evidence, up to 5 proposed fixes, which sources were
  checked, and a link to the investigation in Sidekick when the web UI is
  on. A run that could not finish says so.
- **A newer result edits the same comment**, for example when a run that
  could not finish is run again. A comment that someone deleted is posted
  again.

Text from the alert and from the model is escaped, so it cannot add links,
images, mentions or formatting. The only links are to evidence that the
sources returned and to Sidekick itself.

## How the ticket is found

There is no search. The output uses the first of these that names a ticket
in one of its teams (Linear) or projects (Jira):

1. **A label of the alert** listed in the output's `labels`, holding a
   ticket key such as `OPS-123` or a link to the ticket. For example, an
   Alertmanager rule can set the label `linear_issue: OPS-123`.
2. **A link in the alert** that points at a ticket, such as a Linear issue
   link of the output's workspace or a Jira `/browse/` link of the output's
   site.
3. **A ticket Sidekick created** for the investigation. Ticket actions come
   in a later release; once they exist, the ticket they create is used
   here.

With no such ticket, nothing is posted. A ticket outside the output's
teams or projects is never used, even when the alert names it, so an alert
cannot send a report to just any ticket the credentials can see. A ticket
that moved to another team or project since is skipped too.

## Linear

```yaml
outputs:
  linear-comment:
    type: linear.comment
    apiKeySecret: { name: sidekick-linear, key: apiKey }   # required
    workspace: acme              # required: the URL key in https://linear.app/acme/
    teams: [OPS, PAY]            # required: only issues of these teams get a comment
    labels: [linear_issue]       # optional; this is the default
```

| Key | Required | Meaning |
|---|---|---|
| `apiKeySecret` | yes | A Linear API key. |
| `workspace` | yes | The URL key of the workspace, as in `https://linear.app/<workspace>/`. Issue links of other workspaces are ignored. |
| `teams` | yes | The keys of the teams whose issues may get a comment, such as `OPS`. |
| `labels` | no | The alert labels that may name the issue, in order. Default `[linear_issue]`. |
| `apiURL` | no | The GraphQL endpoint. Default `https://api.linear.app/graphql`. |

To set it up:

1. In Linear, open **Settings**, then **Security and access**, and create a
   personal API key. Give it read access and the permission to create
   comments, limited to the teams in `teams` if your workspace allows it.
   Comments appear under the name of the key's owner, so a dedicated
   account for Sidekick is clearest.
2. Put the key in a Kubernetes Secret, for example `sidekick-linear` with
   the key `apiKey`, and point `apiKeySecret` at it.
3. Add the output and list it in the routes whose alerts have tickets.

Each comment has an ID made from the investigation, so when a request
times out and the publish job tries again, the retry finds the comment the
first try made instead of posting a second one.

## Jira

```yaml
outputs:
  jira-comment:
    type: jira.comment
    url: https://acme.atlassian.net                         # required
    email: sidekick@acme.example                             # required: the account of the API token
    apiTokenSecret: { name: sidekick-jira, key: apiToken }   # required
    projects: [OPS]              # required: only issues of these projects get a comment
    labels: [jira_issue]         # optional; this is the default
```

| Key | Required | Meaning |
|---|---|---|
| `url` | yes | The address of the Jira Cloud site. It must be https. Issue links of other sites are ignored. |
| `email` | yes | The email address of the Atlassian account the API token belongs to. |
| `apiTokenSecret` | yes | An API token of that account. |
| `projects` | yes | The keys of the projects whose issues may get a comment, such as `OPS`. |
| `labels` | no | The alert labels that may name the issue, in order. Default `[jira_issue]`. |

The output uses the REST API version 3 of Jira Cloud and writes the comment
in the Atlassian Document Format, where text is always plain text. Jira
Data Center and Server do not support that format, so they are not
supported yet.

To set it up:

1. Pick the Atlassian account Sidekick comments as; a dedicated account is
   clearest. It needs the project permissions **Browse projects** and
   **Add comments**, and **Edit own comments** so a newer result can edit
   its comment, in each project in `projects`.
2. Create an API token for it at
   <https://id.atlassian.com/manage-profile/security/api-tokens>.
3. Put the token in a Kubernetes Secret, for example `sidekick-jira` with
   the key `apiToken`, and point `apiTokenSecret` at it.
4. Add the output and list it in the routes whose alerts have tickets.

Each comment carries a comment property, `sidekick`, with a key made from
the investigation. Before it posts, the output looks through the issue's
50 newest comments for that key, so when a request times out and the
publish job tries again, the retry edits the comment the first try made
instead of posting a second one.

Jira emails an issue's watchers when a comment is added or edited, so a
comment is edited only when a newer result arrives.

## Failures and retries

A failed request makes the publish job try again with backoff, for about
half an hour. A 429 with a short `Retry-After` is waited out first. Failed
calls are counted in `sidekick_output_api_errors_total`, by output type,
operation and error; see [Running Sidekick](running.md#metrics).
