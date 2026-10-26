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

The model uses the connector that `model.roles.investigate` names, or the
route's own `roles.investigate`. A call that fails because the provider is
busy or rate limited is repeated up to the connector's `retries`.

## Tools

The model sees two built-in tools and the tools of the route's sources:

- **`record_finding`** records one finding while the investigation runs,
  citing the tool calls that support it. Slack shows it in the message's
  progress at once, before the verdict.
- **`submit_verdict`** ends the investigation with the report: the verdict,
  the confidence, the reason, a summary, the findings, and for a real issue
  the cause, the impact and the fixes, or for a false alarm why it fired
  and how to stop it.
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
| `budget` | The investigation used all its tool calls, or reached its time limit. | Not sure yet |
| `error` | The model refused the request for good, for example because it was too long, or the job failed on every attempt. | Could not finish |

The first three end in status `final`; `error` ends in status `failed`.
When the investigation ends without an accepted report, its report holds
the findings recorded with `record_finding` and what was checked.

## Limits

Two limits hold each investigation: the lower of the route's `budgets` and
the strategy's `budgets`, fixed when the investigation is created (see
[`budgets`](configuration.md#budgets)).

- **`maxToolCalls`** counts the calls to source tools. A call past the
  limit is not run; the model is told so, and the investigation ends with
  stop reason `budget`.
- **`maxWallTime`** counts from when the investigation first started,
  including any time a restart took. When it runs out, the call in
  progress is stopped and the investigation ends with stop reason
  `budget`.

`budgets.concurrency` and each route's `concurrency` limit how many
investigations run at once across all replicas; the others wait in the
queue.

## Restarts and several replicas

Every prompt, model answer, tool result and reminder is saved as a step
before the next one starts. If the replica running an investigation stops,
another one takes over the job when its lease runs out (within about a
minute and a half, or at once after a clean shutdown) and goes on from the last saved
step: it repeats the tool calls that were in progress, which are read-only,
and sends the model the whole saved conversation. A replica that lost the
job cannot write any more steps, so two replicas never write the same
investigation.

A job that fails on every attempt (5 by default) ends its investigation as
`failed` with stop reason `error`, and the outputs show "Could not finish"
with what was found.

## Cost

Each model answer is saved with its tokens and its cost, from the price
table (see [the `model` section](configuration.md#model)), and the
investigation keeps the totals. A model without a price is saved without a
cost.
