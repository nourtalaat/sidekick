# Google Cloud source

The Google Cloud source (`type: gcp`) lets investigations read the Google
Cloud projects your services run in: Cloud Logging entries, Cloud
Monitoring metrics and alerts, what changed according to the audit logs,
GKE clusters and their upgrades, and the Google Cloud incidents that
Personalized Service Health says affect a project. It calls the Google
Cloud REST APIs with one identity, and it only reads.

This page lists the smallest set of permissions the source needs and how to
give Sidekick its identity. The source's config keys are in
[Configuration](../configuration.md#google-cloud-sources-type-gcp).

Google also runs remote MCP servers for Cloud Logging, Cloud Monitoring and
GKE, a known alternative once Sidekick's MCP sources can send the OAuth
tokens they need, which expire every hour.

## Permissions

The identity needs these permissions on each project in `projects`, and
nothing else. The simplest way is one custom role with exactly these
permissions:

| Permission | Used by |
|---|---|
| `logging.logEntries.list` | `query_logs`, `list_changes` |
| `monitoring.timeSeries.list` | `query_metrics`, `query_promql` |
| `monitoring.alerts.list` | `list_alerts` |
| `container.clusters.get`, `container.clusters.list` | `get_gke_cluster` |
| `container.operations.list` | `get_gke_cluster`, for the cluster's recent operations |
| `servicehealth.events.list` | `list_service_health_events` |

```sh
gcloud iam roles create sidekickReader --project <project> \
  --title "Sidekick reader" \
  --permissions logging.logEntries.list,monitoring.timeSeries.list,monitoring.alerts.list,container.clusters.get,container.clusters.list,container.operations.list,servicehealth.events.list
```

If you would rather use predefined roles, these cover the same tools but
grant a little more, all of it read-only:

- Logs Viewer (`roles/logging.viewer`)
- Monitoring Viewer (`roles/monitoring.viewer`)
- Kubernetes Engine Cluster Viewer (`roles/container.clusterViewer`). It
  lacks `container.operations.list`, so add that permission with a custom
  role, or `get_gke_cluster` for one cluster fails with a tool error.
- Personalized Service Health Viewer (`roles/servicehealth.viewer`)

What the source can read is what these permissions allow:

- `query_logs` passes the model's filter to Cloud Logging as it is, inside
  the call's time range, and `query_metrics` and `query_promql` pass the
  model's filter and query to Cloud Monitoring the same way. With
  `logging.logEntries.list` on the project, `query_logs` reads every log
  in the project's `_Default` and `_Required` buckets except Data Access
  audit logs, which need Private Logs Viewer
  (`roles/logging.privateLogViewer`); Sidekick does not need that role, so
  do not grant it unless you want those logs searched. To keep some logs
  away from Sidekick, grant access to a log view instead of the project.
- The source asks for tokens with the `cloud-platform` scope, because the
  GKE and Service Health APIs take no narrower one. The IAM roles above are
  what limit it.
- Nothing in the list writes. The source sends only GET requests, and the
  log entry search, a POST request that changes nothing.

Grant the role on each project the source reads, and list exactly those
projects in `projects`. A call for any other project is a tool error, and
nothing is sent to Google Cloud.

## Giving Sidekick an identity

Sidekick finds its identity through Application Default Credentials, so it
never needs a key in its config.

**On GKE**, use Workload Identity Federation for GKE. Either let the
Kubernetes service account act as a Google service account, which the Helm
chart's `serviceAccount.workloadIdentity.gcpServiceAccount` sets up (see
[Cloud credentials](../install.md#cloud-credentials)), and grant the role
to that Google service account:

```sh
gcloud projects add-iam-policy-binding <project> \
  --role projects/<project>/roles/sidekickReader \
  --member serviceAccount:sidekick@<project>.iam.gserviceaccount.com
```

or grant the role to the Kubernetes service account itself, with no Google
service account and no chart setting:

```sh
gcloud projects add-iam-policy-binding <project> \
  --role projects/<project>/roles/sidekickReader \
  --member "principal://iam.googleapis.com/projects/<project number>/locations/global/workloadIdentityPools/<project>.svc.id.goog/subject/ns/sidekick/sa/sidekick"
```

With `networkPolicy.restrictEgress`, allow the GKE metadata server at
`169.254.169.254/32` on port 80, and `*.googleapis.com` on port 443.

**Elsewhere**, such as EKS, AKS or a cluster of your own, use Workload
Identity Federation: create a credential configuration file with
`gcloud iam workload-identity-pools create-cred-config`, put it in a
Secret, and name it in `credentialsSecret`. The file holds no key; it says
where to find the token of the platform's own identity. A service account
key file is accepted too, but it is a long-lived secret, so prefer
federation. Any other file type is refused. Sidekick reads the file again
each time it needs a new token, about once an hour, so a changed file needs
no restart.

```sh
kubectl create secret generic sidekick-gcp --from-file=credentials.json=./sidekick-cred-config.json
```

```yaml
sources:
  gcp:
    type: gcp
    projects: [shop-prod]
    credentialsSecret: { name: sidekick-gcp, key: credentials.json }
```

## Limits

- **Time range.** Every tool reads at most 7 days. `query_logs`,
  `query_metrics` and `query_promql` read the last hour by default, and
  `list_alerts`, `list_changes` and `list_service_health_events` the last
  24 hours.
- **Rows.** `query_logs` and `list_changes` return up to 100 entries by
  default and at most 500, from at most 5 pages. `query_metrics` and
  `query_promql` return up to 20 series by default and at most 100, with
  at most 120 points each. `list_alerts` and `list_service_health_events`
  return up to 30 items by default and at most 100; `list_alerts` reads at
  most 500 alerts to find them. `get_gke_cluster` returns the cluster's 10
  most recent operations.
- **Size.** Log messages, descriptions and error messages are cut at
  2 KiB, and names and labels at 512 bytes. A result stops adding items at
  256 KiB. A result that was cut says so.
