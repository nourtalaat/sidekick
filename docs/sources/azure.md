# Azure source

The Azure source (`type: azure`) lets investigations read the Azure
subscriptions your services run in: Log Analytics logs, and what changed
according to the Activity Log. It calls the Log Analytics query API and
Azure Resource Manager with one identity, and it only reads.

This page lists the smallest set of permissions the source needs and how to
give Sidekick its identity. The source's config keys are in
[Configuration](../configuration.md#azure-sources-type-azure).

## Permissions

The identity needs these actions on each subscription in `subscriptions`,
and on each workspace in `workspaces`, and nothing else. Make them one
custom role:

| Action | Used by |
|---|---|
| `Microsoft.OperationalInsights/workspaces/read`, `Microsoft.OperationalInsights/workspaces/query/*/read` | `query_logs` |
| `Microsoft.Insights/eventtypes/values/read` | `list_changes` |

```json
{
  "Name": "Sidekick Reader",
  "Description": "Read-only access for Sidekick investigations.",
  "Actions": [
    "Microsoft.OperationalInsights/workspaces/read",
    "Microsoft.OperationalInsights/workspaces/query/*/read",
    "Microsoft.Insights/eventtypes/values/read"
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

Do not use the built-in Monitoring Reader or Reader roles instead. They
grant `*/read` on every resource, and Monitoring Reader also grants
`Microsoft.Support/*`, which can open support requests, so it is not
strictly read-only.

What the source can read is what this role allows:

- `query_logs` passes the model's KQL query to Log Analytics as it is,
  with the call's time range, so it can read every table in the
  workspaces you list. To keep some tables away from Sidekick, replace
  `workspaces/query/*/read` with `workspaces/query/<table>/read` for each
  table it may read.
- Every API version the source uses is stable: Log Analytics `v1` and the
  Activity Log `2015-04-01`.
- Nothing in the role writes. The source sends only GET requests, and the
  Log Analytics query, a POST request that changes nothing.

List exactly the subscriptions and workspaces Sidekick should read. A call
for any other subscription, a resource in one, or any other workspace is a
tool error, and nothing is sent to Azure.

## Giving Sidekick an identity

**On AKS**, use Microsoft Entra Workload ID. Create a user-assigned managed
identity, give it the role above, add a federated credential for
Sidekick's Kubernetes service account, and set the identity's client ID in
the Helm chart's `serviceAccount.workloadIdentity.azureClientId` (see
[Cloud credentials](../install.md#cloud-credentials)). The chart adds the
annotation and the pod label the AKS webhook needs, and the source finds
the identity from the variables the webhook sets. No secret is involved.

```sh
az identity create --name sidekick --resource-group <group>
az role assignment create --assignee "$(az identity show -n sidekick -g <group> --query clientId -o tsv)" \
  --role "Sidekick Reader" --scope /subscriptions/<subscription id>
```

With `networkPolicy.restrictEgress`, allow `login.microsoftonline.com`,
`management.azure.com` and `api.loganalytics.io` on port 443.

**Elsewhere**, register an app in Microsoft Entra ID, give its service
principal the role, and create a client secret. Put the secret in a
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

- **Time range.** Every tool reads at most 7 days. `query_logs` reads the
  last hour by default, and `list_changes` the last 24 hours.
- **Rows.** `query_logs` returns up to 100 rows by default and at most 500
  over all tables, with each cell cut at 1 KiB; write the query with
  `take` or `summarize` so it returns what matters. `list_changes` returns
  up to 100 events by default and at most 500, from at most 5 pages.
- **Size.** Error messages are cut at 2 KiB,
  and names at 512 bytes. A result stops adding items at 256 KiB. A
  result that was cut says so.
