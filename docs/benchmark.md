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

Every scenario is checked before anything runs, and every problem is printed
with its file and field. Then each scenario runs `--repeat` times, one run
at a time, and the command prints one line per run:

```
SCENARIO            RUN  STATUS  VERDICT        EXPECTED       STOP       TOOL CALLS  TOKENS  COST     TIME
checkout-pool-size  1/3  final   true_positive  true_positive  submitted  4           3600    $0.0215  4.1s
```

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
  their locks apart from its own. The database role needs `CREATE` on the
  database. The schema's name is logged when it is created; if the process
  is killed before it can drop it, drop it by hand with
  `DROP SCHEMA sidekick_bench_<random> CASCADE`.

### Exit status

| Status | Means |
|---|---|
| `0` | Every run finished. |
| `1` | Something else failed. |
| `2` | A bad flag, config file or scenario file, or, with `--db`, a database secret that cannot be read or a role that may not create the schema. |
| `3` | The model provider is unreachable after the connector's retries, or Docker or the database failed. |

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
