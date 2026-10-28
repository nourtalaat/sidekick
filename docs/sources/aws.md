# AWS source

The AWS source (`type: aws`) lets investigations read the AWS accounts your
services run in: CloudWatch Logs Insights queries over the log groups you
allow, CloudWatch metrics and alarms, what changed according to CloudTrail
event history, EKS clusters and their node groups, ECS services with their
deployments and events, and AWS Health events. It calls the AWS APIs with
aws-sdk-go-v2, in the accounts and Regions you list, and it only reads.

This page lists the IAM policy the source needs and how to give Sidekick
its AWS identity. The source's config keys are in
[Configuration](../configuration.md#aws-sources-type-aws).

## Permissions

The IAM policy of the role Sidekick reads an account with is the hard
limit on what the source can read. Sidekick's own checks, described below,
are a second layer: they catch mistakes, but a policy that is too broad
still lets the source read more than you meant. Attach this policy, and
nothing else, to the role in each account. Replace `<account id>` with the
account's ID and list your own log group prefixes, the same ones as in
`logGroups`:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "SidekickLogsInsightsQueries",
      "Effect": "Allow",
      "Action": "logs:StartQuery",
      "Resource": [
        "arn:aws:logs:*:<account id>:log-group:/ecs/*",
        "arn:aws:logs:*:<account id>:log-group:/aws/eks/prod/*"
      ]
    },
    {
      "Sid": "SidekickReadOnly",
      "Effect": "Allow",
      "Action": [
        "logs:GetQueryResults",
        "logs:StopQuery",
        "logs:DescribeLogGroups",
        "cloudwatch:GetMetricData",
        "cloudwatch:DescribeAlarms",
        "cloudwatch:DescribeAlarmHistory",
        "cloudtrail:LookupEvents",
        "eks:ListClusters",
        "eks:DescribeCluster",
        "eks:ListNodegroups",
        "eks:DescribeNodegroup",
        "ecs:ListServices",
        "ecs:DescribeServices",
        "health:DescribeEvents",
        "health:DescribeEventDetails"
      ],
      "Resource": "*"
    }
  ]
}
```

| Action | Used by |
|---|---|
| `logs:StartQuery`, on the allowed log groups only | `query_logs` |
| `logs:GetQueryResults`, `logs:StopQuery` | `query_logs`, to read its query's results and to stop a query that runs out of time |
| `logs:DescribeLogGroups` | `query_logs`, to find the log groups that start with a prefix such as `/aws/eks/prod/*` |
| `cloudwatch:GetMetricData` | `query_metrics` |
| `cloudwatch:DescribeAlarms`, `cloudwatch:DescribeAlarmHistory` | `list_alarms` |
| `cloudtrail:LookupEvents` | `list_changes` |
| `eks:ListClusters`, `eks:DescribeCluster`, `eks:ListNodegroups`, `eks:DescribeNodegroup` | `get_eks_cluster` |
| `ecs:ListServices`, `ecs:DescribeServices` | `get_ecs_service` |
| `health:DescribeEvents`, `health:DescribeEventDetails` | `list_health_events` |

Use `arn:aws-us-gov:` or `arn:aws-cn:` instead of `arn:aws:` in GovCloud
or China. Do not use the managed `ReadOnlyAccess` or
`CloudWatchLogsReadOnlyAccess` policies instead: they read every log group
and much more.

What the source can read is what this policy allows:

- `query_logs` passes the model's query to Logs Insights as it is, over
  the log groups the call names and the call's time range, so it can read
  every log event in those groups. `logs:StartQuery` is limited to the log
  group prefixes above, so AWS refuses a query on any other log group even
  if Sidekick's own check missed it.
- The Logs Insights `SOURCE` command picks log groups inside the query.
  Sidekick refuses any query that uses it, before anything is sent; the
  policy above stays the real limit.
- Masked log data stays masked: `unmask()` needs `logs:Unmask`, which the
  policy does not grant.
- `logs:StopQuery` stops a Logs Insights query and changes no data. AWS
  counts it as read-only access too: the managed
  `CloudWatchLogsReadOnlyAccess` policy includes it.
- `query_metrics` passes the model's metric math to CloudWatch as it is.
  `SEARCH()` and Metrics Insights queries in an expression can read any
  metric of the account, which `cloudwatch:GetMetricData` allows anyway:
  AWS has no way to limit it to some metrics. In a CloudWatch
  cross-account monitoring account, that includes the metrics of the
  linked source accounts.
- `cloudtrail:LookupEvents` reads the management events of the last 90
  days in one Region. Global services such as IAM log their events in
  us-east-1, so list that Region to see them. Data events, such as S3
  object reads, are not in event history.
- `eks:DescribeCluster` returns the cluster's settings, never credentials,
  and gives no access to the Kubernetes API, which needs an access entry
  in the cluster. Objects inside a cluster are read by the
  [Kubernetes source](kubernetes.md). `get_eks_cluster` and
  `get_ecs_service` take cluster and service names only, not ARNs, since
  an ARN can name another account.
- The AWS Health API needs a Business, Enterprise On-Ramp or Enterprise
  support plan on the account. Without one, `list_health_events` is a tool
  error that says so; the other tools are not affected. The Health API is
  global: Sidekick calls it in us-east-1 (cn-northwest-1 in China,
  us-gov-west-1 in GovCloud) and asks only for events of the call's
  Region and of global services.
- Nothing in the policy writes, and the source sends no request that
  changes anything in AWS.

Logs Insights bills by the data a query scans. The 7-day limit on the time
range and the log group prefixes bound what one call can scan, and each
result reports `bytesScanned`.

List exactly the accounts, Regions and log group prefixes Sidekick should
read. A call for any other account, Region or log group is a tool error,
and nothing is sent to AWS.

## Giving Sidekick an identity

**On EKS**, give Sidekick's service account an IAM role with IRSA or EKS
Pod Identity, as [Cloud credentials](../install.md#cloud-credentials)
shows. The source uses the default AWS credential chain, the same as the
[Bedrock connector](../configuration.md#the-bedrock-provider), so no key is
stored in a Secret. **Elsewhere**, the chain also reads environment
variables, the shared config files and the instance role.

**For the account Sidekick runs in**, attach the policy above to that role
and list the account without `roleARN`. Before the first call, Sidekick
checks with `sts:GetCallerIdentity`, which needs no permission, that its
credentials belong to that account; if they do not, every call for the
account fails.

**For any other account**, create a role there with the policy above, and
let Sidekick's own role assume it. Its trust policy names Sidekick's role
and, if you want one, an external ID:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": { "AWS": "arn:aws:iam::<sidekick account id>:role/<sidekick role>" },
      "Action": "sts:AssumeRole",
      "Condition": { "StringEquals": { "sts:ExternalId": "<external id>" } }
    }
  ]
}
```

Sidekick's own role then needs `sts:AssumeRole` on that role:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "sts:AssumeRole",
      "Resource": "arn:aws:iam::<account id>:role/sidekick-reader"
    }
  ]
}
```

Set the role and the external ID on the account in the config. Sidekick
assumes the role with the session name `sidekick`, keeps its credentials
until shortly before they expire, and then assumes it again:

```yaml
sources:
  aws:
    type: aws
    regions: [us-east-1]
    logGroups: [/ecs/, /aws/eks/prod/]
    accounts:
      - id: "111122223333"   # the account Sidekick runs in
      - id: "444455556666"
        roleARN: arn:aws:iam::444455556666:role/sidekick-reader
        externalID: sidekick-prod
```

With `networkPolicy.restrictEgress`, allow `logs.<region>.amazonaws.com`,
`monitoring.<region>.amazonaws.com`, `cloudtrail.<region>.amazonaws.com`,
`eks.<region>.amazonaws.com`, `ecs.<region>.amazonaws.com` and
`sts.<region>.amazonaws.com` on port 443 for each Region in `regions`,
`health.us-east-1.amazonaws.com` for `list_health_events`, as well as what
IRSA or EKS Pod Identity need.

## Limits

- **Time range.** Every tool reads at most 7 days. `query_logs` needs
  both `since` and `until`; `query_metrics` reads the last hour by
  default, and `list_changes`, `list_alarms` and `list_health_events` the
  last 24 hours.
- **Points.** `query_metrics` takes at most 10 queries and returns up to
  10 series by default and at most 50. Each series has at most 120
  points: the period is at least 60 seconds, and is raised to the
  smallest whole minute that keeps the range within 120 points.
- **Log groups.** One `query_logs` call names at most 20 log groups or
  prefixes, and one query reads at most 50 log groups, the Logs Insights
  limit; a prefix is expanded from at most 2 pages of log group names.
  When more log groups match, the result says so.
- **Query time.** `query_logs` starts the query, asks every second
  whether it has finished, and stops it 3 seconds before the call's
  `timeout`, so the call still answers in time. Raise `timeout` for slow
  queries.
- **Rows.** `query_logs` returns up to 100 rows by default and at most
  500, with each value cut at 1 KiB; write the query with `limit` or
  `stats` so it returns what matters. `list_changes` returns up to 50
  events by default and at most 200, from at most 5 pages of 50 events,
  with each event's request parameters cut at 1 KiB and at most 10
  resources. `list_alarms` returns up to 30 alarms, or state changes of
  one alarm, by default and at most 100. `get_eks_cluster` describes at
  most 10 node groups, and lists at most 100 clusters. `get_ecs_service`
  returns up to 20 service events by default and at most 100, and lists
  at most 100 services. `list_health_events` returns up to 20 events by
  default and at most 100, with the description of the first 10.
- **Size.** Error messages are cut at 2 KiB, and names at 512 bytes. A
  result stops adding items at 256 KiB. A result that was cut says so.
