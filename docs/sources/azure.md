# Azure source

The Azure source (`type: azure`) lets investigations read the Azure
subscriptions your services run in: Log Analytics logs, Azure Monitor
metrics and alerts, what changed according to the Activity Log, Resource
Health and Azure service health, and AKS clusters. It calls the Log
Analytics query API and Azure Resource Manager with one identity, and it
only reads.

This page lists the smallest set of permissions the source needs and how to
give Sidekick its identity. The source's config keys are in
[Configuration](../configuration.md#azure-sources-type-azure).

## Permissions

The identity's role assignments are the hard limit on what the source can
read. Sidekick's own checks, described below, are a second layer: they
catch mistakes, but a role that is too broad still lets the source read
more than you meant. Give the identity two assignments and nothing else.

**On each workspace in `workspaces`, and only there**, assign Log
Analytics Reader, for `query_logs`. Assign it on the workspace itself,
never on its resource group or the subscription, where it would let KQL
read every workspace and every other resource below that scope:

```sh
az role assignment create --assignee <client id> --role "Log Analytics Reader" \
  --scope /subscriptions/<id>/resourceGroups/<group>/providers/Microsoft.OperationalInsights/workspaces/<name>
```

**On each subscription in `subscriptions`**, assign one custom role with
only the actions the other tools need:

| Action | Used by |
|---|---|
| `Microsoft.Insights/metrics/read`, `Microsoft.Insights/metricDefinitions/read` | `query_metrics` |
| `Microsoft.AlertsManagement/alerts/read` | `list_alerts` |
| `Microsoft.Insights/eventtypes/values/read` | `list_changes` |
| `Microsoft.ResourceHealth/availabilityStatuses/read`, `Microsoft.ResourceHealth/events/read` | `get_resource_health` |
| `Microsoft.ContainerService/managedClusters/read` | `get_aks_cluster` |

```json
{
  "Name": "Sidekick Reader",
  "Description": "Read-only access for Sidekick investigations.",
  "Actions": [
    "Microsoft.Insights/metrics/read",
    "Microsoft.Insights/metricDefinitions/read",
    "Microsoft.AlertsManagement/alerts/read",
    "Microsoft.Insights/eventtypes/values/read",
    "Microsoft.ResourceHealth/availabilityStatuses/read",
    "Microsoft.ResourceHealth/events/read",
    "Microsoft.ContainerService/managedClusters/read"
  ],
  "NotActions": [],
  "AssignableScopes": ["/subscriptions/<subscription id>"]
}
```

```sh
az role definition create --role-definition sidekick-reader.json
az role assignment create --assignee <client id> --role "Sidekick Reader" \
  --scope /subscriptions/<subscription id>
```

Do not use the built-in Monitoring Reader or Reader roles on the
subscription instead. They grant `*/read` on every resource, and Monitoring
Reader also grants `Microsoft.Support/*`, which can open support requests,
so it is not strictly read-only.

What the source can read is what these assignments allow:

- `query_logs` passes the model's KQL query to Log Analytics as it is,
  with the call's time range, so it can read every table in a workspace
  you list. To keep some tables away from Sidekick, use a custom role on
  the workspace with `Microsoft.OperationalInsights/workspaces/read` and
  `Microsoft.OperationalInsights/workspaces/query/<table>/read` for each
  table it may read, instead of Log Analytics Reader.
- KQL can reach beyond the workspace it runs on, with `workspace()`,
  `app()`, `resource()`, `adx()`, `arg()`, `cluster()` and `database()`,
  and `externaldata` makes Log Analytics fetch a URL. Sidekick refuses any
  query that uses them, before anything is sent, even when the name only
  appears inside a string. This check reads the query text and does not
  parse KQL, so the role assignment above stays the real limit.
- `query_metrics` passes the model's dimension filter to Azure Monitor as
  it is.
- `managedClusters/read` returns a cluster's settings, never its
  credentials. The source has no call that lists cluster credentials, and
  the role does not allow one. Objects inside a cluster are read by the
  [Kubernetes source](kubernetes.md).
- `list_alerts` uses the Alerts Management API version
  `2023-07-12-preview`: Microsoft publishes no stable version of the alerts
  list. Every other API version the source uses is stable.
- Nothing in either assignment writes. The source sends only GET
  requests, and the Log Analytics query, a POST request that changes
  nothing.

List exactly the subscriptions and workspaces Sidekick should read. A call
for any other subscription, a resource in one, or any other workspace is a
tool error, and nothing is sent to Azure.

## Giving Sidekick an identity

**On AKS**, use Microsoft Entra Workload ID. Create a user-assigned managed
identity, give it the two role assignments above, add a federated credential for
Sidekick's Kubernetes service account, and set the identity's client ID in
the Helm chart's `serviceAccount.workloadIdentity.azureClientId` (see
[Cloud credentials](../install.md#cloud-credentials)). The chart adds the
annotation and the pod label the AKS webhook needs, and the source finds
the identity from the variables the webhook sets. No secret is involved.

```sh
az identity create --name sidekick --resource-group <group>
az identity show -n sidekick -g <group> --query clientId -o tsv   # the <client id> above
```

With `networkPolicy.restrictEgress`, allow `login.microsoftonline.com`,
`management.azure.com` and `api.loganalytics.io` on port 443.

**Elsewhere**, register an app in Microsoft Entra ID, give its service
principal the two role assignments, and create a client secret. Put the secret in a
Kubernetes Secret and set `tenantID`, `clientID` and `clientSecret`.
Sidekick reads the secret again each time it needs a new token, about once
an hour, so a rotated secret needs no restart. A client secret is a
long-lived credential, so prefer Workload ID wherever you can.

```sh
kubectl create secret generic sidekick-azure --from-literal=client-secret='<secret>'
```

```yaml
sources:
  azure:
    type: azure
    subscriptions: [<subscription id>]
    tenantID: <tenant id>
    clientID: <client id>
    clientSecret: { name: sidekick-azure, key: client-secret }
```

## Limits

- **Time range.** Every tool reads at most 7 days. `query_logs` and
  `query_metrics` read the last hour by default, and `list_alerts`,
  `list_changes` and the service health events of `get_resource_health`
  the last 24 hours.
- **Rows.** `query_logs` returns up to 100 rows by default and at most 500
  over all tables, with each cell cut at 1 KiB; write the query with
  `take` or `summarize` so it returns what matters. `list_changes` returns
  up to 100 events by default and at most 500, from at most 5 pages.
  `query_metrics` returns up to 10 series by default and at most 50, with
  at most 120 points each, for at most 10 metrics. `list_alerts` returns
  up to 30 alerts by default and at most 100.
- **Size.** Descriptions, summaries and error messages are cut at 2 KiB,
  and names at 512 bytes. A result stops adding items at 256 KiB. A
  result that was cut says so.
