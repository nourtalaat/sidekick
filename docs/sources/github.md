# GitHub source

The GitHub source (`type: github`) lets investigations see what changed in a
service's repository: recent commits, pull requests, releases and GitHub
Actions runs, and the files at any ref. It reads through the GitHub REST
API as a GitHub App installation, and it only reads.

This page lists the smallest set of permissions the source needs and how
to set the app up. The source's config keys are in
[Configuration](../configuration.md#github-sources-type-github).

## Permissions

Give the app these **repository permissions**, all **Read-only**, and
nothing else:

| Permission | Used by |
|---|---|
| Metadata | Every tool. GitHub gives it to every app. |
| Contents | `list_commits`, `get_commit`, `compare`, `get_file`, and the releases and tags of `list_releases`. |
| Pull requests | `list_pull_requests`. |
| Actions | `list_workflow_runs`. |

The app needs no organization or account permissions, and no webhook.

Sidekick asks GitHub for installation tokens with exactly these four
permissions, read-only, so a token never gets more even if the app is
later given more. GitHub refuses a token for permissions the app does not
have, so the app needs all four, even when you hide a tool with
`tools.deny`.

Install the app only on the repositories Sidekick should read. The source's
`repos` setting limits the tools further, but the installation is what
GitHub enforces.

Keep this app for investigations only. An action that opens pull requests
needs write access, and should use a separate app installed only on the
repository it changes, so the investigation source never holds write
access.

## Setting up the app

1. Create a GitHub App in the organization that owns the repositories
   (**Settings**, **Developer settings**, **GitHub Apps**, **New GitHub
   App**). Give it a name and a homepage URL, clear **Webhook: Active**,
   and set the four repository permissions above to **Read-only**. Allow
   installation **Only on this account**.
2. Note the **App ID** on the app's settings page.
3. Under **Private keys**, generate a private key. GitHub downloads a
   `.pem` file.
4. Install the app (**Install App**), choose **Only select repositories**,
   and pick the repositories Sidekick should read. The installation ID is
   the number at the end of the installation's settings page URL, for
   example `https://github.com/organizations/acme/settings/installations/7890123`.
5. Store the private key in a Kubernetes Secret:

   ```sh
   kubectl create secret generic sidekick-github \
     --from-file=private-key.pem=./sidekick.2026-05-04.private-key.pem
   ```

6. Add the source to `sidekick.yaml`:

   ```yaml
   sources:
     github:
       type: github
       app:
         appID: 123456
         installationID: 7890123
         privateKeySecret: { name: sidekick-github, key: private-key.pem }
       repos: ["acme/*"]
       serviceRepos:
         checkout-api: acme/checkout
   ```

To rotate the key, generate a new one, update the Secret, then delete the
old key on the app's settings page. Sidekick reads the key each time it
makes a token, so it needs no restart.

## GitHub Enterprise Server

Set `baseURL` to your server's REST API root, for example
`https://github.example.com/api/v3/`. Everything else is the same.

## When something is wrong

- **Every call fails** with a message about the installation token: the
  app ID, installation ID or private key is wrong, the app lacks one of
  the four permissions, or the clock of the node Sidekick runs on is off by
  more than a minute.
- **A tool reports "Resource not accessible by integration"**: the app is
  not installed on that repository, or lacks the permission that tool
  uses.
- **A tool finds no data for a repository you expect**: GitHub answers
  "not found" both for a repository that does not exist and for one the
  installation cannot see.
