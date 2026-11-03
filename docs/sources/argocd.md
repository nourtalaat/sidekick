# The Argo CD source

The Argo CD source (`type: argocd`) lets an investigation see the state of
the applications Argo CD deploys: whether an application is in sync with
its repository, whether it is healthy, whether Argo CD syncs it by itself
(auto-sync), what was deployed and when, and who started each deploy. It
calls the Argo CD REST API with one API token, and it only reads. This page
lists its keys, its tools and the exact access the token needs.

It reads what is deployed. Changing an application, such as rolling it back,
is a separate action that has a token of its own.

## Config

```yaml
sources:
  argocd:
    type: argocd
    baseURL: https://argocd.example.com
    tokenSecret: { name: sidekick-argocd, key: token }
    applications:
      - shop/*
      - platform/ingress-*
```

| Key | Default | Meaning |
|---|---|---|
| `baseURL` | none | The address of your Argo CD server, with its sub-path when it is served from one, such as `https://example.com/argocd`. Required. It must use `https`; plain `http` is accepted only for `localhost` and loopback addresses, because the token is sent with each request. |
| `tokenSecret` | none | `{ name, key }` of the Secret that holds the API token. Required. |
| `applications` | none | The applications the tools may read, as `project/name` globs, which is how Argo CD RBAC names them. Required, at least one. A call for any other application is a tool error, and nothing is sent to Argo CD. |
| `namespace` | `argocd` | The namespace Argo CD runs in, and so the namespace of the applications the tools read. Applications in other namespaces are not read. |

Project and application names are lowercase. In a glob, `*` matches any
run of characters, `?` one character and `[a-c]` one character of a set.
`*` does not match `/`, so `shop/*` allows the applications of the project
`shop` and nothing else. An application of the default project is
`default/<name>`.

The shared source keys (`tools.allow`, `tools.deny`, `tools.rename`,
`timeout` and `concurrency`) work as for every source; see
[`sources`](../configuration.md#sources). `timeout` may be at most `60s`.

## Tools

The model sees each tool as `<source>__<tool>`, for example
`argocd__get_history`.

| Tool | Returns |
|---|---|
| `list_applications` | The applications the config allows: name, project, destination (cluster and namespace), sync status, health, whether auto-sync is on, the revision deployed now, the revision Argo CD last compared the application with, when it last synced, and a link. It can filter by `project`, by a `name` pattern, and by `state`: `out_of_sync`, `not_healthy`, or `problem` (either). |
| `get_application` | One application: its source (repository, path, target revision, Helm or Kustomize settings), destination, project, sync policy with a plain sentence that says whether auto-sync is on (and the prune and self-heal settings), sync status and health, the last operation (phase, message, times, who started it), whether an operation is running, conditions, the images in use, and up to 20 resources that are not synced or not healthy. |
| `get_history` | The deployment history of one application, newest first: history ID, revision, deploy time, who started it (`user:<name>` or `automated`), the source, and for each of the newest 10 the commit message, author, date and tags of its revision. The newest entry is marked `current`: it is what runs now. |

`get_application` and `get_history` take `application` as `project/name`,
for example `shop/checkout`. Take it from the `application` field of
`list_applications`. A name without its project is a tool error, because
Argo CD can only tell "no such application" from "not allowed" when it
knows the project.

How it behaves:

- **Start-up never waits for Argo CD.** The first tool call is the first
  request. The token is read from its Secret for each request, so a rotated
  token needs no restart, and it is never logged.
- **Auto-sync.** Auto-sync is on when the application's automated sync
  policy is set and not switched off with `enabled: false`, which is how
  Argo CD itself decides. While it is on, Argo CD refuses a rollback, and
  the next sync would undo it.
- **Two revisions.** `deployedRevision` is the revision of the newest
  successful sync. `comparedRevision` is the revision Argo CD last compared
  the application with, such as the newest commit of its target branch. With
  auto-sync off, an out-of-sync application has a compared revision that is
  ahead of the deployed one.
- **History length.** Argo CD keeps as many history entries as the
  application's `revisionHistoryLimit`, 10 by default, so older deployments
  are not there to find. Only a successful sync of the whole application adds
  an entry, so the newest entry is what is deployed. A rollback adds a new
  entry. An application with `revisionHistoryLimit: 0` has no history.
- **Several sources.** Entries of an application with `sources` list their
  sources and revisions, and have no commit.
- **Resource health.** Since Argo CD 3.0, Argo CD does not keep the health
  of each resource in the application unless `controller.resource.health.persist`
  is `true`. `get_application` still gets it from Argo CD's cache; when Argo
  CD gives none, the result says so, and lists only the resources that are not
  synced. `list_applications` shows the health of whole applications only.
- **Empty results.** An application Argo CD does not have, or that is not in
  the project named, and a list without matches, return no data, not an
  error. An application without history returns no data with a link.
- **Errors the model can act on.** An application outside `applications`, a
  name that is not `project/name`, a wrong `state`, a name pattern that does
  not parse, and a request Argo CD refuses with a 4xx status, such as a 403
  for an application the token may not get, are tool errors.
- **Failures.** A refused or expired token (401), rate limits (429), server
  errors, an Argo CD that cannot be reached and an empty token Secret fail
  the call. After 5 failures in a row the source is skipped for 60 seconds,
  as for every source.
- **Evidence.** Results link to the application's page in the Argo CD UI,
  `<baseURL>/applications/<namespace>/<name>`.

## Limits

- `list_applications` returns up to `limit` applications (default 50, at
  most 200), ordered by project and name, and says how many matched in all.
  Argo CD answers a list with the full status of every application it may
  show, so give `applications` as few projects as you can: when every glob
  names its project, Sidekick asks Argo CD for those projects only. An answer
  larger than 32 MiB fails the call.
- `get_history` returns at most 50 entries. It reads the commit of each of
  the newest 10 entries from Argo CD, 5 at a time. A commit Argo CD cannot
  read, for example because it cannot reach the repository, is left out, and
  the result counts how many are missing.
- `get_application` lists up to 20 resources that are not in order and says
  how many there are, and up to 10 conditions. It shows Helm parameters as
  `name=value`, up to 20, and never the inline Helm values.
- Argo CD keeps Helm parameters in plain text, and teams do put passwords in
  them. Helm parameters whose names look like secrets are shown without their
  values, as `name=[hidden]`: the value is hidden when the last dotted
  segment of the name, in any case, contains `password`, `passwd`, `secret`,
  `token`, `key`, `credential`, `auth` or `cert`. So `db.password`, `apiKey`,
  `tls.key` and `auth.existingSecret` are hidden, while `image.tag` and
  `keycloak.url` are shown. This holds for the source of an application and
  for every source in its history.
- Messages, commit messages and names from Argo CD are cut at 2 KiB or 256
  bytes. They are written by people and systems, so they are passed to the
  model as untrusted text, like every source result.

## Access token

Sidekick sends only `GET` requests, to the application list, one
application and the revision metadata of an application. All three need one
Argo CD RBAC permission (RBAC is role based access control): `get` on
`applications`, for the `<project>/<name>` objects in `applications`. Give the
token that and nothing else: no `sync`, no `override`, no `update`, no
`create`, no `delete`, no `action` and no `logs`.

Make a local account that can only create API tokens. Add it to the
`argocd-cm` ConfigMap:

```yaml
data:
  accounts.sidekick: apiKey
```

Or a person can run:

```sh
kubectl patch -n argocd configmap argocd-cm --type merge \
  -p '{"data":{"accounts.sidekick":"apiKey"}}'
```

`apiKey` lets the account have API tokens; it does not let it sign in.

Then give it the permission in the `argocd-rbac-cm` ConfigMap, with one
`p` line for each glob in `applications`:

```yaml
data:
  policy.csv: |
    p, role:sidekick-read, applications, get, shop/*, allow
    p, role:sidekick-read, applications, get, platform/ingress-*, allow
    g, sidekick, role:sidekick-read
```

Argo CD gives every signed-in account the role in `policy.default`, and a
`deny` rule cannot take it away. Check that `policy.default` is empty or a
role without rights, because the stock `role:readonly` can read every
application. Policies use glob matching unless `policy.matchMode` is `regex`;
with `regex`, write the policy as a regular expression.

Make a token for the account and put it in the Secret that `tokenSecret`
names. A person runs these, signed in to Argo CD as an admin; Sidekick
never runs `argocd`:

```sh
argocd account generate-token --account sidekick
kubectl create secret generic sidekick-argocd --from-literal=token=<token>
```

The token never expires by default. `argocd account generate-token` takes
`--expires-in` for one that does; make a new token before it ends and update
the Secret.

To check what the token may do, a person can run (with the token in
`ARGOCD_AUTH_TOKEN`):

```sh
argocd account can-i get applications shop/checkout
argocd account can-i sync applications shop/checkout
```

The first answers `yes` and the second `no`.

A project role works too, and keeps the rights inside one project. Create the
role and a token for it with `argocd proj role create`,
`argocd proj role add-policy` and `argocd proj role create-token`, with a
policy of `-a get -p allow -o '*'` (`applications, get, shop/*`). A project
role covers one project, so a source that reads several projects needs a token
for each of them or an account.

About names and namespaces:

- Argo CD names an application `<project>/<name>` in RBAC when it is in the
  namespace Argo CD runs in. With "applications in any namespace" turned on,
  an application of another namespace is `<project>/<namespace>/<name>`, and
  a glob such as `shop/*` can match it too, because Argo CD's glob does not
  stop at `/`. Sidekick asks for the namespace in `namespace` and ignores
  applications of any other.
- The source was written against Argo CD 3.5. Older servers work, but an
  Argo CD before 2.11 does not say who started a deploy, and one before 3.1
  has no `enabled: false` for automated sync.
