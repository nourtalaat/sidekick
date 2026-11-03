# GitLab source

The GitLab source (`type: gitlab`) lets investigations see what changed in a
service's project on gitlab.com or on a self-managed GitLab: recent commits,
merge requests, releases, pipelines and deployments, and the files at any
ref. It reads through the GitLab REST API with one access token, and it only
reads.

This page lists the config keys, the tools, the limits, and the smallest
access the token needs. The config keys are also in
[Configuration](../configuration.md#gitlab-sources-type-gitlab).

## Config

```yaml
sources:
  gitlab:
    type: gitlab
    tokenSecret: { name: sidekick-gitlab, key: token }
    projects: ["acme/checkout", "acme/platform/*", "infra/**"]
    serviceProjects:
      checkout-api: acme/checkout
```

| Key | Default | Meaning |
|---|---|---|
| `tokenSecret` | none | `{ name, key }` of the Secret that holds the access token. Required. |
| `projects` | none | The projects the tools may read, as full paths that may hold globs. Required, at least one. A call for any other project is a tool error, and nothing is sent to GitLab. |
| `serviceProjects` | none | Maps a service name to its project, so a tool can be called with the service an alert names. Each project must be one that `projects` allows. |
| `baseURL` | `https://gitlab.com` | The root of your GitLab, for example `https://gitlab.example.com`, or `https://example.com/gitlab` when GitLab is served under a path. Do not add `/api/v4`. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses, because the token is sent with each request. |

A GitLab project lives in a group, and often in subgroups, so a project path
has two or more parts: `group/project` or `group/subgroup/project`. The globs
in `projects` follow the parts of the path:

- `*` and `?` stay inside one part, so `acme/*` allows `acme/checkout` but
  not `acme/platform/api`.
- `**` is a whole part that matches any number of parts, so `acme/**` allows
  every project under `acme`, at any depth.
- `**` alone allows every project the token can read, so the token's own
  access is then the only limit.

Paths are matched without regard to case, as GitLab matches them. Give a
project by its full path, not by its numeric ID, because an ID cannot be
checked against `projects`.

The shared source keys (`tools.allow`, `tools.deny`, `tools.rename`,
`timeout` and `concurrency`) work as for every source; see
[`sources`](../configuration.md#sources). `timeout` may be at most `60s`.

## Tools

The model sees each tool as `<source>__<tool>`, for example
`gitlab__list_commits`. Every tool takes `project` (the full path) or
`service` (a name from `serviceProjects`), and only reads.

| Tool | Returns |
|---|---|
| `list_commits` | Commits since a time, newest first, optionally on a ref or touching a path. |
| `get_commit` | One commit: message, author, parents, changed files with line counts, and patches. |
| `list_merge_requests` | Merge requests opened or merged since a time, with state, branches, author, times, merge commit and link. |
| `list_releases` | Releases with their dates, and optionally the newest tags. |
| `list_pipelines` | Pipelines, newest first, with status, source, ref, commit, times and link. |
| `get_file` | A file at a ref, or the entries of a directory. |
| `compare` | The newest commits of one ref that another lacks, and the changed files. |
| `list_deployments` | Deployments, newest first, optionally of one environment, with status, ref, commit, who deployed, times and link. |

`list_deployments` has no twin in the GitHub source. GitLab records each
deployment to an environment, so this tool says what was deployed where and
when more clearly than the pipeline list does.

How it behaves:

- **Start-up never waits for GitLab.** The first tool call is the first
  request. The token is read from its Secret for each request, so a rotated
  token needs no restart, and it is never logged. It is sent in the
  `PRIVATE-TOKEN` header.
- **Project.** A project that `projects` does not allow, an unknown service,
  a numeric ID, or a ref or path that does not look like one is a tool error,
  and nothing is sent to GitLab. GitLab addresses a project by its path with
  each `/` written as `%2F`; Sidekick sends it that way, and no project or
  path a model gives can reach another project.
- **Results** are JSON, and link to the GitLab pages of the commits, merge
  requests, releases, pipelines, deployments and files they name, which
  become evidence links in the report.
- **No data.** A project, ref or path GitLab does not have, and an empty
  list, are reported as no data. GitLab gives the same "not found" for a
  project the token cannot see.
- **Errors.** Other refusals, such as a ref GitLab cannot resolve or a role
  that is too low for the call (a 400 or a 403), go to the model as tool
  errors with GitLab's message. A refused token (401), rate limits (429),
  server errors and a GitLab that cannot be reached fail the call. After 5
  failures in a row the source is skipped for 60 seconds, as for every
  source.
- **Redirects.** GitLab answers a request for a project that moved with a
  redirect. Sidekick follows it only to the same host and scheme as `baseURL`,
  so the token never goes to another host. Change the path in `projects` to
  the new one.

## Limits

- Lists return up to `limit` items (default 30, at most 100) and read at
  most 10 pages. Merge request lists read at most 30 per page, because their
  descriptions can be long.
- Commit messages in lists are cut at 1 KiB, and the message of one commit at
  16 KiB. The patches of one commit are cut at 64 KiB in all, and
  `get_commit` and `compare` list at most 300 changed files. A result that
  was cut says so.
- GitLab gives no line counts for a file, so Sidekick counts the added and
  removed lines in the patch. When GitLab leaves a file's diff out because it
  is too large or was collapsed, the file shows `omitted` with the reason and
  its counts are 0.
- `compare` compares from the merge base of the two refs, so it lists the
  commits of `head` that `base` lacks, newest first, up to `limit`.
  `totalCommits` is how many there are. It does not say how far `base` is
  ahead of `head`. When GitLab gives up part of a large comparison, the
  result has `timedOut` set and its files may be incomplete.
- `get_file` cuts text at 256 KiB, leaves out binary files and files over
  1 MB, and lists at most 1000 directory entries. It tries the path as a
  file first and as a directory when there is no such file.
- `list_merge_requests` asks GitLab for the merge requests updated since the
  time, then keeps those opened or merged since then, so a merge request that
  only got a comment is left out.
- `list_deployments` keeps the deployments created since the time. GitLab
  gives no link for a deployment, so Sidekick builds the link to its page
  from the environment and the deployment number.
- Titles, messages and descriptions are written by people, so they are passed
  to the model as untrusted text, like every source result.

## Access token

The source needs a token with the **`read_api`** scope and the **Reporter**
role, and nothing more.

- **Scope.** `read_api` reads the API. `read_repository` reads only Git and
  the repository files, so it is not enough for merge requests, pipelines,
  releases and deployments. Do not use `api`, which can change things.
- **Role.** Reporter can view the code, merge requests, pipelines,
  environments and releases of a project. Guest cannot read the code of a
  private project. From GitLab 18.7, the Planner role can read private code
  too, but Reporter works on every version.

A token can be one of three kinds. Any of them works; choose by how widely
the token should read. Making a project access token takes the Maintainer or
Owner role in the project, and a group access token the Owner role in the
group.

- **Project access token.** It reads one project. In the project, go to
  **Settings**, **Access tokens**, **Add new token**, give it a name, choose
  the role **Reporter** and the scope `read_api`, and create it.
- **Group access token.** It reads every project in the group and its
  subgroups. In the group, go to **Settings**, **Access tokens**, **Add new
  token**, choose the role **Reporter** and the scope `read_api`.
- **Personal access token.** It reads what its user can read. Make a
  dedicated user for Sidekick, or a service account if your GitLab has them,
  and add that user to the projects or groups as **Reporter**. Signed in as
  that user, select your avatar, **Edit profile**, **Access tokens**, and add
  a token with the scope `read_api`.

The steps are the same on gitlab.com and on a self-managed GitLab; on a
self-managed GitLab, set `baseURL` to its root. Tokens expire: the default
and, unless an administrator changes it, the longest lifetime is 365 days, so
plan to rotate the token before then.

Put the token in the Secret that `tokenSecret` names:

```sh
kubectl create secret generic sidekick-gitlab --from-literal=token=glpat-...
```

The `projects` setting limits the tools further, but what GitLab enforces is
the token's own access. Make the token read only the projects Sidekick should
see.

A GitLab behind a reverse proxy must pass the encoded `%2F` in a path on as
it is. A proxy that decodes it makes GitLab answer 404 for every project.

## When something is wrong

- **Every call fails with "GitLab answered 401"**: the token is wrong, was
  revoked, or has expired. Make a new one and update the Secret.
- **A tool finds no data for a project you expect**: GitLab answers "not
  found" both for a project that does not exist and for one the token cannot
  see. Check the path, and that the token's user or the token itself has
  access to the project.
- **A tool reports "GitLab answered 403"**: the token lacks the scope or the
  role for that call, or the project has turned off the feature, such as the
  repository or pipelines.
- **Every call finds no data on a self-managed GitLab behind a proxy**: the
  proxy may decode `%2F` in paths. Configure it to pass the path through
  unchanged.
- **A call fails with a message about a redirect**: the project moved to a
  new path or another address. Use the new path in `projects`, and check that
  `baseURL` is the address GitLab itself uses.
- **A call fails with "GitLab answered 429"**: GitLab's rate limit for the
  token. Give Sidekick its own token, so other tools do not use up the limit.
