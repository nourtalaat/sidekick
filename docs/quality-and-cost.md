# Quality and cost

This page groups the guides that explain how Sidekick reaches a verdict,
how to measure how good it is, and how to keep the cost low.

## How investigations run

- [How investigations run](investigations.md): the agent loop, the report, and how an investigation ends.
- [Limits](investigations.md#limits): how much one investigation may do.
- [Cost](investigations.md#cost): the tokens and cost saved with each model answer.
- [Restarts and several replicas](investigations.md#restarts-and-several-replicas): what happens when Sidekick restarts mid-investigation.
- [Lessons](investigations.md#lessons): short guidance that people reviewed and that later investigations get.
- [Budgets](configuration.md#budgets): the limits of one investigation, and the daily caps and rate limits.
- [Strategies](configuration.md#strategies): what an investigation checks first for one kind of event.

## The benchmark

- [Benchmarks](benchmark.md): `sidekick bench run` and what it measures.
- [Running scenarios](benchmark.md#running-scenarios): the command and the Postgres it needs.
- [The public scenarios](benchmark.md#the-public-scenarios): the scenarios that ship with Sidekick.
- [Scores](benchmark.md#scores): the scores a run gets and how each is worked out.
- [The strategy gate](benchmark.md#the-strategy-gate): how a proposed strategy change is checked on the scenarios before anyone applies it.
- [Exporting a real investigation](benchmark.md#exporting-a-real-investigation): how to turn one into a scenario.

## Free setups

- [Run it for free](free-setups.md): the three free ways to run Sidekick, ranked.
- [A local model](free-setups.md#1-a-local-model): keeps your data on your network.
- [OpenRouter free models](free-setups.md#2-openrouter-free-models): needs an OpenRouter account.
- [The Gemini API free tier](free-setups.md#3-the-gemini-api-free-tier): needs a Google AI Studio key.
- [Published scores](free-setups.md#published-scores): the default model's scores, and how to measure a free setup yourself.
