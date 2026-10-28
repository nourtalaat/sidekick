# How investigations run

Every event a route takes opens an investigation. This page describes what
the investigation does, how it ends, the limits it runs within, and what
happens when Sidekick restarts in the middle of one.

## The loop

The investigation runs on one replica at a time, as a background job. It
sends the model the event, the route's strategy and the tools it may call,
then loops:

1. The model answers. It can call tools, several at once.
2. Sidekick runs the calls, at most 4 at a time, and sends the results
   back. Each result is redacted and marked as data from outside Sidekick,
   which the model is told never to follow as instructions.
3. The loop goes on until the model submits its report, or a limit is
   reached.

Events that join the investigation's group while it runs, and recoveries
of its alerts, wait in the investigation's inbox. Before the model's next
answer, Sidekick sends everything that waits as one message, with what the
sender wrote marked as data from outside Sidekick. A message shows at most
20 events in full and counts the rest. A recovery is evidence, for example
that the alert cleared on its own; it never stops the investigation.
Each change of a [watcher's](configuration.md#watch) state waits in the
inbox too, such as a watcher that started or recovered, and so does a
route or strategy watcher that a limit kept from starting. The message
lists them after the events, with the result of each watcher's last check
marked as data from outside Sidekick. Like a recovery, a watcher's result
is evidence and never decides the verdict by itself.

The model uses the connector that `model.roles.investigate` names, or the
route's own `roles.investigate`. A call that fails because the provider is
busy or rate limited is repeated up to the connector's `retries`.

## Tools

The model sees the built-in tools and the tools of the route's sources:

- **`record_finding`** records one finding while the investigation runs,
  citing the tool calls that support it. Slack shows it in the message's
  progress at once, before the verdict.
- **`submit_verdict`** ends the investigation with the report: the verdict,
  the confidence, the reason, a summary, the findings, and for a real issue
  the cause, the impact and the fixes, or for a false alarm why it fired
  and how to stop it.
- **`start_watcher`** starts a [watcher](configuration.md#watch), a
  read-only check that runs on its own schedule and tells when the service
  is back up. It is offered only when `watch.allowModel` is on and the
  route does not turn it off, and only with the check types, tools and
  hosts the route's `watcherLimits` allow. A request outside them gets an
  error naming the limit, and nothing starts. The watcher keeps running
  after the investigation ends.
- **Source tools** are named `<source>__<tool>`, for example
  `signoz__search_logs`. A route's `sources` list limits them to those
  sources; without one, every source's tools are offered. See
  [the `sources` section](configuration.md#sources) for which tools a
  source shows.

## The report

Sidekick checks every report the model submits before it accepts it:

- A real issue needs a cause, an impact and at least one fix.
- A false alarm needs why it fired and at least one change that stops it,
  and at least the confidence `report.minFalsePositiveConfidence` (default
  `high`). Below it, the model must say "not sure yet" instead.
- Every finding may cite only tool calls that ran earlier in the
  investigation and returned data.

A report that fails a check goes back to the model with the reasons, and
the model tries again, up to 3 times in all.

The model never writes links. The evidence of a finding is taken from the
calls it cites: the links the source returned for them, or a reference to
the call when the source returned none. A finding that cites no call is
shown as a hypothesis.

Sidekick adds which sources were checked (with `ok`, `no_data`, `failed` or
`unavailable`) and why the investigation stopped; the model cannot set
either.

## How an investigation ends

| Stop reason | When | Verdict |
|---|---|---|
| `submitted` | The model submitted a report that passed its checks. | The model's |
| `no_verdict` | The report failed its checks 3 times, or the model ended its turn without calling a tool 3 times (after 2 reminders). | Not sure yet |
| `budget` | A budget ran out: tokens, tool calls, time or cost, or a daily cap was reached. | Not sure yet |
| `error` | The model refused the request for good, for example because it was too long, or the job failed on every attempt. | Could not finish |
| `cancelled` | A responder pressed Stop. | Not sure yet |

The first three end in status `final`, `error` ends in status `failed`, and
`cancelled` in status `cancelled`.
When the investigation ends without an accepted report, its report holds
the findings recorded with `record_finding` and what was checked.

## Limits

Four limits hold each investigation: the lower of the route's `budgets` and
the strategy's `budgets`, fixed when the investigation is created (see
[`budgets`](configuration.md#budgets)). What each has used is saved with
every step, so a restarted investigation goes on counting.

- **`maxTokens`** counts the input tokens, including those read from or
  written to the prompt cache, and the output tokens of every model call.
- **`maxToolCalls`** counts the calls to source tools. A call past the
  limit is not run, and the model is told so.
- **`maxWallTime`** counts from when the investigation first started,
  including any time a restart took.
- **`maxCostUSD`** counts the cost of the model calls, from the price
  table.

When 90% of the tokens, cost or tool calls are used, or a minute before
the time limit (a tenth of it when the limit is under 10 minutes), the
model gets one last turn. It is told the budget is nearly used, and
`submit_verdict` is the only tool it may call; where the model's provider
allows it, the call is required. A report it submits then keeps its
findings, cause and fixes, but its verdict becomes "Not sure yet", with
stop reason `budget` and the budget in the stop detail, which also names
the verdict the model submitted.

The investigation ends without that last turn, with a report built from
the findings recorded so far, when the last turn does not submit a report
that passes its checks, when the time limit passes, or when the tokens or
cost are used up before the last turn could start. Either way
`sidekick_budget_exhausted_total` counts it, with the budget as its
`budget` label.

A [daily cap](configuration.md#daily-caps) is checked before an
investigation starts and before each model call. Before it starts, a cap
that is reached holds it until the next day or ends it at once, as
`onDailyCap` says; once it runs, a cap that is reached gives the model
its last turn as above, with `daily_cap` as the budget.

`budgets.concurrency` and each route's `concurrency` limit how many
investigations run at once across all replicas; the others wait in the
queue. Follow-up runs count too.

## Follow-up questions

A [follow-up question](slack.md#follow-up-questions) is answered in a new
run of the investigation after the first run ends. The run starts from
the saved steps, so the model sees the whole first run, then the
questions that waited, in one message, with who asked each one. It has
the same tools, and `answer_follow_up` to answer without changing the
verdict; it calls `submit_verdict` with a full new report when the answer
changes the verdict. Questions are what to look at: they cannot change the
tools, the action mode or the verdict rules. Questions that arrive while a
run is going on wait for the next run.

Each follow-up run has half of each of the investigation's limits, at
least 1 of a limit that is set, and counts only its own use: its tokens,
tool calls and cost, and the time from its own start. Its last turn offers
only `answer_follow_up`. A run that ends without an answer leaves the
report as it was, with an answer that says why. A key in the config to set
the follow-up limits can come later if it is needed.

The model's reasoning is sent back to it in later runs only when the
investigation's connector is still of the same provider type; reasoning
from another provider is left out. When the saved conversation is over
70% of the model's context size, the oldest long tool
results are replaced by short summaries from the `summarize` role, or the
`investigate` connector when no `summarize` role is set, until it fits.
The summaries are saved as steps; the original results stay saved.

## Restarts and several replicas

Every prompt, model answer, tool result, reminder and inbox message is
saved as a step before the next one starts. If the replica running an investigation stops,
another one takes over the job when its lease runs out (within about a
minute and a half, or at once after a clean shutdown) and goes on from the last saved
step: it repeats the tool calls that were in progress, which are read-only,
and sends the model the whole saved conversation. A replica that lost the
job cannot write any more steps, so two replicas never write the same
investigation.

A job that fails on every attempt (5 by default) ends its investigation as
`failed` with stop reason `error`, and the outputs show "Could not finish"
with what was found.

A Stop pressed in Slack reaches the run from any replica: it is saved in
the database, and every replica is told at once. The replica running the
investigation stops the model or tool call in progress, and the
investigation ends with stop reason `cancelled` and a report built from the
saved steps. A replica that missed the message sees the Stop at the run's
next step.

## Cost

Each model answer is saved with its tokens and its cost, from the price
table (see [the `model` section](configuration.md#model)), and the
investigation keeps the totals. A model without a price is saved without a
cost.

Each answer's cost and tokens are also added to the day's spend of its
route, connector and model, in the same database write as the answer, so
the [daily caps](configuration.md#daily-caps) always see what was spent.
The day starts at midnight in `budgets.daily.timeZone`. The gauge
`sidekick_daily_spend_usd`, with a `route` label, shows what each route
has spent today, read from the database at each scrape, so every replica
shows the same total.
