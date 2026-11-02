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
| `3` | The model provider is unreachable after the connector's retries for the runs, or Docker or the database failed. A judge call that fails this way is a judge error on its run, not exit status `3`. |

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

## The strategy gate

Sidekick can propose a change to a strategy when people keep rating it wrong
(see [`strategies.proposals`](configuration.md#proposed-changes)). The
benchmark is how it checks the change before anyone applies it: the gate runs
the scenarios that cover the strategy with the current version and with the
change, and lets the change pass only when it is not clearly worse. There is no
way to apply a change that did not pass.

**Which scenarios.** A scenario covers a strategy when it names it in
`strategy`, or names none (or `generic-alert`) and the strategy's `match` fits
its event. The gate chooses from the scenario files under
`bench.gate.publicDir` and the [reviewed scenarios](#exporting-a-real-investigation)
of the database together; a scenario that is still a draft is never chosen. When
a scenario of the directory and a reviewed one have the same ID, the one from the
directory is used. With neither source, or none that covers the strategy, the
gate ends with `no_benchmark_coverage`. It runs each chosen scenario with the strategy
under test. A scenario that is not valid is left out with a warning in
the log.

**The two runs.** The current version and the change each run every chosen
scenario `bench.gate.minRepeats` times, with the same models, in turn. The
runs are graded by the judge as `sidekick bench run` grades them, and both are
stored as benchmark runs with the trigger `gate`, so they show in the
[dashboard](web-ui.md#dashboard-numbers-and-benchmark-runs).

**The pass rule.** For each scenario the gate takes the mean overall score of
the change minus that of the current version. It resamples those differences
with replacement 10,000 times to get a 95% interval for their mean. The change
`passes` when the lower end of the interval is above minus
`bench.gate.maxDrop` (0.02 by default): it is not clearly worse. Comparing
two single averages would let a neutral change pass or fail by chance, and a
slightly worse one pass on a lucky run.

| Result | Means |
|---|---|
| `passed` | The lower end of the interval is above the allowed drop. |
| `failed` | The change scores lower on average by more than the allowed drop. |
| `not_enough_evidence` | The change looks no worse on average, but the interval is too wide to be sure, or fewer than 3 scenarios cover the strategy. Run more repeats or add scenarios. |
| `no_benchmark_coverage` | No scenario covers the strategy. Export confirmed investigations of it as scenarios. |
| `over_budget` | The gate stopped before a run that would pass `bench.gate.maxCostUSD`. |
| `error` | The gate could not finish, for example because the database role may not create a schema. |

At least 3 scenarios must cover the strategy (a constant in the code,
`MinGateScenarios`), because resampling one or two differences shows almost no
spread. With fewer, the gate ends at once with `no_benchmark_coverage` or
`not_enough_evidence` and spends nothing. Both runs, the interval, its seed and
the cost are stored with the proposal, so a reviewer sees how sure the result
is and the interval can be worked out again.

**What it costs.** A gate makes `2 x minRepeats x (the number of scenarios)`
investigations and the judge's grading of each. With 5 repeats and 8 scenarios
that is 80 investigations. The cost of each counts in `spend_daily` under the
route `bench`, with the investigate connector and model, so it shows in the
dashboard and counts toward `budgets.daily.maxCostUSD`; while that cap is
reached, a gate waits and carries on later. `bench.gate.maxCostUSD` caps one
gate.

**It survives a restart.** A gate can run for a long time. Every finished run is
kept, together with its cost, so a gate that is interrupted by a restart, by
a provider that is down or by a daily cap carries on from the runs it finished
and does not pay for them again. If the chosen scenarios changed in the
meantime, it starts over.

**Needs.** The gate runs in a schema of its own in the database of
`database.urlSecret`, which it drops at the end. The database role needs
`CREATE` on the database for that; this is the same need as `sidekick bench
run --db`. A `summarize` role must be set to draft the changes at all.

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

A judge call that fails because the provider is overloaded, rate limited or
slow is repeated like an investigation's call: up to the connector's
`retries` times, after the wait the provider asks for, or a growing wait
(from 1 second, doubling, up to 1 minute, with some random spread). With
the default of no retries, one such failure counts at once. When a grade
still fails after its retries, `bench run` does not stop: that run is a
judge error, with `rootCause.error` set in `report.json`, a score of 0 and
no match, and it counts in the `judge errors` total. The command writes the
report, the JUnit file and the summary as usual, and the thresholds decide
the exit status. A judge that is down for many runs therefore fails the
`rootCauseMatch` threshold; read the `judge errors` row before you trust a
low root-cause score. A model call that is refused (for example a bad
request) is a judge error as well.

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
  thresholds, and each scenario with its spread and runs. A run lists the
  reason it gave for its verdict and, when it ended without a verdict, the
  detail of why (`reason`, `stopDetail`), and the source tools it called, in
  order (`tools`). Each tool entry holds only the tool's name and the status
  of the call, never its arguments or its result text. Its schema is
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

## Exporting a real investigation

A confirmed investigation can become a scenario, so the benchmark covers
the alerts your own systems raise. An admin exports it, Sidekick cleans it,
and an admin reviews it. Only a reviewed scenario can be run or downloaded.

1. **Export.** Use `sidekick bench export --investigation <id>`, or
   `POST /api/v1/investigations/{id}/scenario-draft` (admin). The
   investigation must have a verdict that people confirmed. The command prints the
   scenario's ID and keeps the draft in the database, with the status
   `draft`. Asking again for the same investigation returns the same
   scenario.
2. **Review.** An admin opens the draft, reads it next to the original, edits
   it, and ticks "I checked this for sensitive data" before it becomes
   `reviewed`. The review needs a signed-in person; an API token cannot do
   it. Any edit puts a reviewed scenario back to `draft`.
3. **Use.** `sidekick bench export --scenario <id> --out <file>` writes a
   reviewed scenario to a file, and `GET /api/v1/scenarios/{id}/file`
   downloads it. Both refuse a draft. Put the file in a scenario directory
   and run it like any other scenario.

```sh
sidekick bench export --investigation 0198f2a4-7c3e-7b1d-9a52-3f6e8d1c4b70
sidekick bench export --scenario latency-alert-checkout-api-p99-3f6e8d1c4b70 --out bench/scenarios/private/checkout-pool.yaml
```

`bench export` reads the config file for the database
(`database.urlSecret`), the intake endpoints, the redaction settings and the
`summarize` model. The database must have every migration (`sidekick migrate`).
With `--scenario` it needs `--out`, and it refuses to replace a file unless
you pass `--force`. It exits with `0` on success, `1` for a draft or for an
investigation that cannot be exported, `2` for bad flags, a bad config file or
an ID that does not exist, and `3` when the database cannot be reached.

### What the draft holds

- **The alert.** The payload of the first alert of the investigation, as
  the intake adapter received it (after the redaction that runs when data
  is stored), with the adapter of the endpoint it came in on. An
  investigation that a Slack or Teams message opened has no payload to
  replay and cannot be exported, nor can one whose webhook endpoint is no
  longer in the config.
- **The sources.** One fixture for each recorded call to a source. A
  fixture keeps only the arguments that identify the query (service,
  repository, namespace, pod, resource, commit and similar) and ignores
  time ranges, limits and the model's wording of a free-text query. Calls
  with the same match share one fixture whose answers come in call order.
  A call that failed, timed out or was refused is left out. A source that
  was unavailable stays unavailable, and the scenario expects the report to
  say so. The tool list of a source is read from `bench/tools/<source
  type>.json`; `bench export --scenario` warns when the file is not in
  the current directory.
- **The outcome.** The confirmed verdict, and the confirmed cause as the
  expected cause.
- **The rubric.** Rubric items drafted by the model of
  `model.roles.summarize` from the cleaned cause. Without a `summarize` role
  the draft has no rubric items, Sidekick says so once at start-up, and the
  reviewer writes them. The model call is priced and counted in the daily
  spend of the investigation's route and in the daily caps like any other
  model call; when a cap is reached the draft is made without rubric items.
  The model sees only the cleaned cause.

Checks that need your judgment, such as which tools a good run calls
(`expected.evidence`), are not drafted. Add them in the editor.

### How the draft is cleaned

The draft goes through a stricter pass than the redaction that runs when
data is stored. Every email, user name, account ID, host name and IP address
that Sidekick can find anywhere in the draft is replaced by a numbered
placeholder: `host-1`, `user-2`, `account-1`, `ip-3`. The same value always
gets the same placeholder in the alert, the fixtures and the expected cause,
so the story still reads. Then all the built-in [redaction](configuration.md#redaction)
rules run over the result, IP addresses included, and the draft is refused
if anything that looks like an email, a host name or an IP address is left.
The same check runs when someone saves an edit and when someone reviews.

Sidekick finds these values in three ways: by their shape (emails, IP
addresses, URLs, and dotted names that end in a domain such as `.com`, `.io` or
`.internal`), by the key they sit under (`host`, `instance`, `user`,
`author`, `account_id`, `tenant`, `project` and similar, in JSON, in
`key=value` pairs, and in URL query strings), and by what it has already
found: once a value is known, it is replaced everywhere, also inside URLs,
inside JSON that is written as a string, and inside log lines. Values in
URL-encoded form are replaced too.

### What the cleaner cannot catch

Please read this before you tick the box.

- A host name with no dot that sits under no known key, for example
  `db-prod-3` written once in a log line, stays. If the same name appears
  anywhere else as a host, it is replaced everywhere.
- A person's name in free text, for example "Rhea restarted the pod", stays,
  unless the same name sits under a key such as `author` or `user`
  somewhere in the scenario.
- Names of companies and products stay on purpose, because they are the
  evidence: repository names, service names, namespaces, team names, and the
  owner in a GitHub or GitLab URL.
- Opaque IDs stay: commit hashes, trace IDs, UUIDs and customer numbers.
  Replacing them would break links and checks. The review lists the
  UUIDs, long mixed tokens and code host owners it kept as places to look at.
- A secret in a shape that no built-in rule or custom rule knows stays.
- Some harmless text is replaced: a version such as `10.0.19.1` looks like an
  IP address, and a word such as `socket.io` looks like a host name.

This is why a person reviews. The review page puts each part of the scenario
next to the recorded original it was made from while the investigation's
events and steps are kept (the retention job deletes them later), and it
lists the places the cleaner kept but could not classify.

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


## The benchmark workflow

`.github/workflows/bench.yml` runs the public scenarios in
`bench/scenarios/public` with the project's Anthropic key, with the config
`bench/sidekick.yaml` (Claude Sonnet 5.5 for the investigation and the
judge, with `retries: 5`, and the built-in strategies) and the thresholds of `bench/bench.yaml`.

| When | What runs |
|---|---|
| By hand (**Run workflow**) | The scenarios, with `repeat` as the number of runs of each. |

The workflow is built for a public repository:

- **By hand only.** The workflow has no `schedule`, `push` or
  `pull_request` trigger, so a pull request, a push, a release tag or the
  clock never starts it, and neither can a fork or a stranger. A maintainer
  starts it, for example before a release, or to benchmark a change on its
  branch. You can also run the command below with your own key. The workflow
  does not use `pull_request_target`.
- **The key.** It is the repository secret `BENCH_ANTHROPIC_API_KEY`, and
  only the job that runs the benchmark sees it. When the secret is not set,
  the workflow skips the run with a notice in the log and the job summary
  and does not fail.
- **Permissions.** The workflow has one job, which can only read the
  repository.
- **Actions** are pinned by commit SHA.

Each run uploads the `bench-report` artifact, with `report.json`
([schema](bench-report.schema.json)) and `junit.xml`, and adds the
Markdown summary to the job summary. The run fails when a threshold in
`bench/bench.yaml` is missed. When `bench/baseline.json` exists, the
workflow also compares the run with it and fails when the overall score
falls more than 0.015 below the baseline's, which puts the floor at an
overall score of 0.90. The workflow sets `maxDrop` to the baseline's overall
score minus 0.90, so change it whenever the baseline changes.

Every run calls a paid model for each scenario three times, so each
run costs money; the `maxCostPerRunUSD` threshold and each scenario's
`limits` bound it. Run the same command by hand with your own key:

```sh
sidekick bench run --scenarios bench/scenarios/public --config bench/sidekick.yaml \
  --bench-config bench/bench.yaml --repeat 3 --out report.json --markdown summary.md
```

with the key in `bench/.secrets/anthropic/apiKey`, which git ignores.
