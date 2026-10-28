# Benchmarks

`sidekick bench run` runs benchmark scenarios: recorded alerts with mock
sources that answer the investigation's tool calls from fixtures, so a run
needs no access to real systems and gives the same tool results every time.
Use it to see how a model, a strategy or a prompt change does on alerts
whose cause you know.

## Running scenarios

```sh
sidekick bench run --scenarios bench/scenarios/sample --config sidekick.yaml --repeat 3
```

| Flag | Default | Does |
|---|---|---|
| `--scenarios`, `-s` | required | A scenario file, or a directory: every `.yaml` and `.yml` file under it is a scenario. |
| `--config`, `-c` | `/etc/sidekick/sidekick.yaml` | The config file. Runs use its model connectors, roles and prices, its strategies, its report rules, its redaction settings and its per-investigation limits. They do not use its sources, outputs, webhooks, routes, rate limits or daily caps. |
| `--repeat`, `-n` | `3` | How many times each scenario runs. |
| `--db` | off | Run on the database of `database.urlSecret` instead of a temporary Postgres; see below. |
| `--bench-config` | none | A `bench.yaml` with the weights of the overall score and the thresholds; see below. |
| `--seed` | `1` | Seeds the resampling of the 95% intervals, so the same results always give the same intervals. |
| `--label` | none | A name for the run, kept in `report.json`. |
| `--out` | none | Write the results to this `report.json`. |
| `--junit` | none | Write the results as JUnit XML, for CI systems that show test results. |
| `--markdown` | none | Write a Markdown summary, for a pull request comment. |

Every scenario is checked before anything runs, and every problem is printed
with its file and field. Then each scenario runs `--repeat` times, one run
at a time, and the command prints one line per run:

```
SCENARIO            RUN  STATUS  VERDICT        EXPECTED       CORRECT  ROOT CAUSE  EVIDENCE  STOP       TOOL CALLS  TOKENS  COST     TIME
checkout-pool-size  1/3  final   true_positive  true_positive  yes      0.80        1.00      submitted  4           3600    $0.0215  4.1s
```

Once every run has finished, the judge grades each run's root cause and
the command prints the spread of each scenario and the run totals, then
the thresholds, if `--bench-config` sets any.

Each run takes the path an alert takes in `sidekick serve`: the payload goes
through the scenario's intake adapter and is stored, the router opens a
group and an investigation on a route named `bench`, and the investigation
runs with the scenario's mock sources, strategy and limits. Each run is an
investigation of its own; the next run of the same scenario does not join
it.

### Postgres

Benchmarks always run on Postgres, with the same code as `serve`.

- **Without `--db`**, `sidekick bench run` starts Postgres 16 in a
  container with Docker (through testcontainers) and removes it when the
  runs end, also after Ctrl-C. You need only Docker. A run that is killed
  hard leaves the container to the testcontainers clean-up container,
  which removes it shortly after.
- **With `--db`**, the runs use the database that `database.urlSecret`
  names, in a schema of their own called `sidekick_bench_<random>`. The
  schema is dropped, with everything in it, when the runs end, also after
  Ctrl-C or an error, so nothing is left behind. A Sidekick that serves from
  the same database never sees the runs' events and jobs, and the runs keep
  their locks apart from its own. The schema's name is logged when it is
  created; if the process is killed before it can drop it, drop it by hand
  with `DROP SCHEMA sidekick_bench_<random> CASCADE`.

  Once the runs are scored, the results are stored in the database's
  default schema, the one `sidekick serve` uses, so the web UI can show
  them: one `bench_runs` row for the run, with its settings, totals and
  thresholds, and one `bench_results` row per run of a scenario. They are
  written in one transaction, through a connection of their own, apart
  from the runs' schema. The default schema must have every migration of
  this Sidekick: if it does not, the command exits with `2` before any run
  and asks you to run `sidekick migrate` first. It never migrates the
  default schema itself. A run that fails stores nothing.

  The database role needs `CREATE` on the database, for the runs' schema,
  and `INSERT` on `bench_runs` and `bench_results` in the default schema.

### Exit status

| Status | Means |
|---|---|
| `0` | Every run finished and every threshold is met. |
| `1` | A threshold failed, or something else failed. |
| `2` | A bad flag, config file, `bench.yaml`, baseline or scenario file, or, with `--db`, a database secret that cannot be read, a role that may not create the schema, or a default schema that needs `sidekick migrate`. |
| `3` | The model provider is unreachable after the connector's retries, for the runs or for the judge, or Docker or the database failed. |

When a run fails, the command prints the runs that finished before it,
unscored, and writes no output files.

## Checking scenario files

`sidekick bench validate` checks scenario files the way `bench run` does
before its first run: the file format, the strategy, the intake adapter and
its payload, and each mock source with its tool snapshot and fixtures. It
prints every problem with its file and field. It calls no model and needs
neither Postgres nor Docker.

```sh
sidekick bench validate --scenarios bench/scenarios/public --config sidekick.yaml
```

When every scenario is valid it prints `scenarios checked: 13, problems: 0`.
It takes `--scenarios` and `--config`, as `bench run` does. The config is read
for its strategies and model connectors, so the secrets of the connectors
must be readable; nothing is sent to a provider. It exits with `0` when every
scenario is valid and `2` for a bad flag, config file or scenario file.

## The public scenarios

`bench/scenarios/public` is the set that the project's own benchmark runs:
13 scenarios about invented companies, services and data, so nothing in them
comes from a real system. Together they cover every verdict, each of the four
built-in strategies, triage skipping a duplicate, a source that is down, and
the first MCP sources (SigNoz, Datadog, Sentry and Linear). Each has a
rubric for the judge and, except the triage one, checks of the run made in
code.

| Scenario | Expected verdict | Strategy | Sources |
|---|---|---|---|
| `latency-ledger-index-dropped` | `true_positive` | `latency-alert` | `prometheus`, `github`, `postgres` |
| `latency-low-traffic-false-positive` | `false_positive` | `latency-alert` | `prometheus`, `github` |
| `error-rate-signoz-bad-secret-reference` | `true_positive` | `error-rate-alert` | `signoz` (MCP), `k8s`, `github` |
| `error-rate-scanner-traffic-false-positive` | `false_positive` | `error-rate-alert` | `elasticsearch`, `prometheus`, `github` |
| `error-rate-datadog-duplicate-of-incident` | `duplicate` | `error-rate-alert` | `datadog` (MCP), `github` |
| `source-failure-logs-unavailable` | `true_positive` | `error-rate-alert` | `elasticsearch` (down), `prometheus`, `launchdarkly`, `github` |
| `service-down-oom-after-limit-change` | `true_positive` | `service-down` | `k8s`, `prometheus`, `github` |
| `service-down-planned-maintenance` | `false_positive` | `service-down` | `k8s`, `github` |
| `sentry-regression-after-release` | `true_positive` | `generic-alert` | `sentry` (MCP), `github` |
| `known-issue-linear-export-timeout` | `known_issue` | `generic-alert` | `linear` (MCP), `github`, `prometheus` |
| `flapping-disk-usage-threshold` | `flapping` | `generic-alert` | `prometheus`, `k8s` |
| `triage-duplicate-second-monitor` | `duplicate` | `generic-alert` | `linear` (MCP), `prometheus` |
| `inconclusive-metrics-gap` | `inconclusive` | `generic-alert` | `prometheus`, `k8s`, `github` |

- Every scenario runs with the strategy it names, but when the config sets a
  `model.roles.triage` role, triage runs first and may choose another
  strategy. `triage-duplicate-second-monitor` is the scenario for triage: its
  alert says that an incident already covers it, so triage can end the run
  with `duplicate` and no tool call. It has no evidence checks, because a run
  that triage ends calls no tool. Without a triage role, a full investigation
  finds the incident in Linear.
- `source-failure-logs-unavailable` marks its `elasticsearch` source as
  unavailable, and the report must still name the cause and list that source
  under checked as unavailable.
- Each MCP source's scenario uses the tool snapshot that
  [its docs page](sources/signoz.md) describes: `bench/tools/signoz.json`,
  `datadog.json`, `sentry.json` and `linear.json`.
- The scenario files are validated by a unit test on every pull request, and
  by `sidekick bench validate`.

## Scores

Each run gets these scores:

| Score | How |
|---|---|
| Verdict correct | The verdict is `expected.verdict` or one of `expected.acceptable`. |
| Root cause | The judge grades each rubric item as met or not met, with a one-line reason. The score is the weight of the items met over the weight of all items. The root cause matches when the score is at least 0.7 and every `required` item is met. A run that ended without a report is not sent to the judge, and scores 0. |
| Evidence | The share of `expected.evidence` checks that pass, checked in code: a `calledTool` check passes when the run called the tool, a `citedURL` check when a finding of the report cites the URL. |
| Source failures reported | Whether every source in `expected.unavailableReported` is listed as unavailable under checked. |
| Cost, time, tokens, tool calls | From the investigation's steps. The judge's own cost is counted apart. |

Each scenario shows its spread over its runs: how many of them got the
verdict right (k of N), the mean and standard deviation of the root-cause
score, and the smallest, median and largest cost and time.

The run totals are:

| Total | How |
|---|---|
| Verdict accuracy | The share of runs whose verdict is correct. |
| Precision and recall | For the false positive and the true positive class, by expected verdict, over all runs. False positive precision comes first, because a wrong false positive hides a real incident. A class no run gave, or no scenario expects, has no value (`-`). |
| Root cause match | The share of graded runs whose root cause matches, and the mean root-cause score. |
| Evidence | The mean evidence score of the runs with evidence checks. |
| Overall score | `0.5 × verdict accuracy + 0.3 × root cause match + 0.2 × evidence`, with the weights of `bench.yaml`. A part with nothing to score is left out and the other weights count for its share. |

Each share has a 95% interval, from resampling the scenarios with
replacement 1,000 times. When two runs' intervals of a metric overlap,
the difference between them may be chance: `sidekick bench compare` flags
it as `no clear difference`.

### The judge

The judge is the model of `model.roles.judge`, or of
`model.roles.investigate` when no judge role is set. It gets the expected
root cause, the rubric and the final report, never the transcript, and
answers with a `submit_grades` tool call. It is forced to call the tool
where the connector can force one, and grades at temperature 0 where the
model takes a temperature (not on the Anthropic API or OpenAI's reasoning
models, which reject one). Grades that miss an item or name an unknown
one go back to it to fix, up to three times; a run the judge gives no
grades for counts as a judge error and does not match. The judge's prompt
has a version, which each `report.json` records.

## bench.yaml

```yaml
weights: { verdict: 0.5, rootCause: 0.3, evidence: 0.2 }   # the defaults
thresholds:
  verdictAccuracy: 0.8      # the lowest share allowed
  fpPrecision: 0.95
  rootCauseMatch: 0.7
  maxCostPerRunUSD: 0.5     # the highest mean cost of a run, in US dollars
  baseline: bench/baseline.json   # a report.json to compare with
  maxDrop: 0.02             # how far the overall score may fall below the baseline's
```

Every key is optional, and a threshold that is not set is not checked. A
metric with nothing to count, such as false positive precision when no run
gave a false positive verdict, passes. The baseline check fails when the
overall score is more than `maxDrop` (0.02 by default) below the
baseline's, and says whether the two intervals show a clear difference.
Paths are read from the directory `sidekick bench run` runs in.

## Output files

- `--out report.json` holds what the run used (Sidekick version and
  commit, a hash of the config file, the scenario set, the connector and
  model settings of each role, the strategy versions, and the prompt and
  judge prompt versions), the run totals with their intervals, the
  thresholds, and each scenario with its spread and runs. Its schema is
  [bench-report.schema.json](bench-report.schema.json); `schemaVersion`
  changes whenever the report changes in a way an older reader would
  misread.
- `--junit junit.xml` has one test suite per scenario, with one test case
  per run, and a suite of the thresholds. A run fails when its verdict is
  wrong or its root cause does not match.
- `--markdown summary.md` is a summary for a pull request comment: the
  thresholds, the run totals and one row per scenario.

## Scenario files

```yaml
id: checkout-pool-size                 # unique in a run
title: Checkout p99 latency after a commit that shrank the DB pool
tags: [latency, database]
strategy: generic-alert                # default: generic-alert
now: 2026-05-04T10:20:00Z              # the scenario clock
route:
  mode: read-only                      # the only mode until actions arrive
event:
  adapter: alertmanager                # the intake adapter type
  config: {}                           # the adapter's own settings, if it has any
  payload:                             # the webhook body, written as YAML, sent as JSON
    version: "4"
    groupKey: '{}:{alertname="CheckoutLatencyHigh"}'
    alerts:
      - status: firing
        labels: { alertname: CheckoutLatencyHigh, service: checkout-api }
        startsAt: 2026-05-04T10:12:00Z
        fingerprint: 6f1c2a9b3d4e5f60
sources:
  github:
    tools: { snapshot: bench/tools/github.json }
    fixtures:
      - tool: list_commits
        match:
          args:
            repo: { equals: acme/checkout }
            since: { within: 6h }
        respond:
          text: "abc123 2026-05-04T10:05Z 'Lower DB pool size to 5'"
          evidence:
            - { kind: commit, title: abc123, url: "https://github.com/acme/checkout/commit/abc123" }
      - tool: list_workflow_runs
        responses:
          - text: "run 991 deploy checkout-api 10:06Z success"
          - noData: true
  k8s:
    unavailable: true
default: no_data                       # or error
expected:
  verdict: true_positive
  acceptable: [true_positive]
  rootCause:
    summary: The DB connection pool ran out after commit abc123.
    rubric:
      - { id: pool, text: Names connection pool exhaustion as the cause, weight: 2, required: true }
  evidence:
    - { calledTool: github__list_commits }
    - { citedURL: "https://github.com/acme/checkout/commit/abc123" }
  unavailableReported: [k8s]
limits: { maxToolCalls: 30, maxCostUSD: 0.50 }
```

`bench/scenarios/sample` holds this scenario in full.

- `now` is the time the event arrives at, and the time `within` matchers
  count from.
- The payload must give exactly one event, and not a recovery.
- `limits` take `maxTokens`, `maxToolCalls`, `maxWallTime` and
  `maxCostUSD`. A limit the scenario does not set takes the config's
  `budgets.perInvestigation` value, and the strategy's own limits still
  apply.
- `expected` is what the runs will be scored against. `verdict` is
  required; `evidence` checks each name a `calledTool` or a `citedURL`, and
  `unavailableReported` names sources of the scenario.
- Unknown keys are errors, so a misspelled key does not silently do
  nothing.

### Mock sources

Each entry under `sources` is a mock source, named as the model sees it:
`github` above gives tools such as `github__list_commits`.

- `tools.snapshot` is a JSON file of the tools the real source lists, as
  `{"tools": [{"name", "description", "inputSchema"}]}`. A relative path is
  read from the directory `sidekick bench run` runs in. The files in
  `bench/tools` are kept the same as the built-in sources' tools by their
  tests. The model sees only these tools, so a call to any other tool never
  reaches the mock.
- `fixtures` answer the calls. The fixtures whose `tool`, a name or a glob
  such as `get_*`, matches the call are tried in file order, and the first
  one whose argument matchers all hold answers. Arguments a fixture does not
  name are ignored, unless it sets `strict: true`.
- `respond` gives one answer to every matching call. `responses` gives its
  answers in order, one per matching call, and repeats the last one. Each
  answer has `text`, `noData: true` for a result that found nothing, and
  `evidence` links with a `kind` (such as `commit`, `metric`, `log_query`,
  `deploy` or `resource`), a `title` and a `url`.
- A call that no fixture matches gets the `default`: `no_data`, a normal
  result with no data and the text "No data for this query", or `error`, a
  tool error. A source can set its own `default`.
- `unavailable: true` makes the source fail to list its tools and every
  call, as a source that is down does. The report lists it as unavailable
  under checked. Such a source needs no snapshot.

| Matcher | Holds when the argument |
|---|---|
| `equals: <value>` | is the value. Numbers compare by value, so `5` equals `5.0`. |
| `regex: <expression>` | is a string the expression matches somewhere. Use `^` and `$` for all of it and `(?i)` to ignore case. |
| `contains: <value>` | is a string with the text in it, or a list with the value in it. |
| `oneOf: [<value>, ...]` | equals one of the values. |
| `exists: true` / `exists: false` | is there, or is not there. |
| `gte: <number>` / `lte: <number>` | is a number at least, or at most, the value. |
| `within: <duration>` | is an RFC 3339 time no further than the duration, such as `2h`, from `now`, before or after it. |

One matcher can set several operators, and all of them must hold.

## Comparing two runs

`sidekick bench compare a.json b.json` shows two `report.json` files of
`sidekick bench run --out` side by side: what each run used (Sidekick
version and commit, the scenario set, the connector and model of each
role, the prompt and judge prompt versions and the strategy versions),
each metric with the change from the first run to the second, and each
scenario's runs.

```
METRIC                    A        B        CHANGE    DIFFERENCE
overall score             0.55     0.87     +0.32     clear difference
verdict accuracy          0.50     0.83     +0.33     no clear difference
```

A share whose 95% intervals overlap in the two runs is flagged
`no clear difference`: the change may be chance, and more scenarios or
more repeats would tell. The command exits with `2` when a report cannot be
read or has another schema version, and `0` otherwise.

## Checking the judge

`sidekick bench judge-check` has the judge grade a set of reports that a
person graded by hand, and shows how far its grades agree, item by item.
Run it when you change the judge model, to see how far its grades move.

```sh
sidekick bench judge-check --config sidekick.yaml
```

| Flag | Default | Does |
|---|---|---|
| `--cases` | `bench/judge-check` | A case file, or a directory: every `.json` file under it is a case. |
| `--config`, `-c` | `/etc/sidekick/sidekick.yaml` | The config file whose judge model grades the cases. |
| `--min-agreement` | `0` | The share of items, from 0 to 1, the judge must grade as the hand grades do. Below it the command exits with `1`. |

The judge is the model of `model.roles.judge`, or of
`model.roles.investigate` when no judge role is set. Pick a judge from a
different model family than the investigate model when you have one, so
the judge does not share the blind spots of the model it grades. The
judge must be able to call tools.

Each case holds an expected root cause with its rubric, a report, and the
hand grade of each rubric item:

```json
{
  "id": "pool-no-commit",
  "rootCause": {
    "summary": "The DB connection pool ran out after commit abc123 lowered the pool size to 5.",
    "rubric": [
      {"id": "pool", "text": "Names connection pool exhaustion as the cause", "weight": 2, "required": true},
      {"id": "commit", "text": "Links the cause to commit abc123 or the 10:05 deploy", "weight": 2, "required": true}
    ]
  },
  "report": {"verdict": "true_positive", "confidence": "medium", "summary": "...", "reason": "...", "cause": "...", "findings": []},
  "grades": {"pool": true, "commit": false}
}
```

The command prints one line per case, with the items the judge graded
otherwise, then the share of items it agreed on and the mean difference
between its root-cause scores and the hand-graded ones.

| Status | Means |
|---|---|
| `0` | The judge graded every case and agreed with at least `--min-agreement` of the items. |
| `1` | It agreed with fewer. |
| `2` | A bad flag, config file or case file. |
| `3` | The model provider is unreachable after the connector's retries. |

