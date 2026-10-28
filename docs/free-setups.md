# Run it for free

Sidekick works with a model that costs nothing, to try it, to run it at
home or to evaluate it on test data. This page ranks three ways to do that,
from the one that keeps your data and gives the most control to the one
that is easiest to start, and shows how to set each one up. All three use
the same `model` section of `sidekick.yaml`; only the connector changes.
The rest of the [quick start](install.md#quick-start) stays as it is.

A free model is slower and less reliable than a paid one. It misses
evidence more often, ends its turns without a verdict more often, and
needs more of the investigation's tool-call budget. The
[benchmark](benchmark.md) measures that gap, and the
[table at the end](#published-scores) shows it for each setup when
scores have been published.

| | Setup | Cost | Your alerts leave your network? | Needs |
|---|---|---|---|---|
| 1 | [A local open-weight model](#1-a-local-model) | None | No | A GPU with enough memory for the model and a long context |
| 2 | [OpenRouter free models](#2-openrouter-free-models) | None, after a one-time purchase | Yes, to OpenRouter and the model's host | An OpenRouter account |
| 3 | [The Gemini API free tier](#3-the-gemini-api-free-tier) | None | Yes, and Google may read and train on it | A Google AI Studio key |

## 1. A local model

Run an open-weight model on a machine you own and point Sidekick at it. No
alert, log line or report leaves your network, and there is no rate limit
but your hardware. Sidekick ships presets for two models, so you need no
`capabilities` block:

| Model | Memory for the weights |
|---|---|
| Qwen3.8-27B | About 18 to 20 GB at 4-bit |
| Gemma 4 31B | About 18 to 20 GB at 4-bit |

Both call tools and have a 256K token context. Run them with a context of
at least 65,536 tokens, which needs memory on top of the weights: an
investigation does not fit in less, and a server that cuts a long prompt
gives a bad answer without an error. Pick the
server that suits you.

### With vLLM

vLLM honours a forced tool choice, which makes investigations end with a
verdict more reliably, so it is the better server. Start it with tool
calling on and a long enough context:

```sh
vllm serve Qwen/Qwen3.8-27B --max-model-len 65536 \
  --enable-auto-tool-choice --tool-call-parser qwen3_xml --reasoning-parser qwen3
```

For Gemma 4 31B use `google/gemma-4-31B-it` with `--tool-call-parser gemma4
--reasoning-parser gemma4`. Then add the connector:

```yaml
model:
  connectors:
    local:
      provider: openai
      model: Qwen/Qwen3.8-27B
      baseURL: http://vllm.inference.svc:8000/v1
      server: vllm
  roles:
    investigate: local
```

### With Ollama

Ollama is the easiest to start, but it ignores a forced tool choice, so
Sidekick reminds the model to call `submit_verdict` when a turn ends
without it. Ollama also picks a small context from the video memory unless
you set one, so start it with:

```sh
OLLAMA_CONTEXT_LENGTH=65536 ollama serve
ollama pull qwen3.8:27b
```

```yaml
model:
  connectors:
    local:
      provider: openai
      model: qwen3.8:27b
      baseURL: http://ollama.inference.svc:11434/v1
      server: ollama
  roles:
    investigate: local
```

Check the `CONTEXT` column of `ollama ps`. `sidekick doctor` checks it for
you while the model is loaded, warns when the context is below the 65,536
tokens the preset needs, and prints the `server:` line to add if you left
it out.

Without `apiKeySecret`, Sidekick sends no key, and plain `http` works to
any host. [Local models for free setups](configuration.md#local-models-for-free-setups)
has the llama.cpp server and the rest of the details. Local models have no
price, so they count as free in the cost budgets; a time limit
(`budgets.perInvestigation.maxWallTime`) is what keeps a slow model in
check.

## 2. OpenRouter free models

[OpenRouter](https://openrouter.ai) serves many models through one
OpenAI-compatible API, and some of them are free: the ones whose ID ends in
`:free`. OpenRouter limits free models to 20 requests a minute and 50 a day
for an account that has bought less than $10 of credit, and to 1,000 a day
for one that has bought $10 or more once. One investigation makes a
request for each step, so the 50 a day allows only a few investigations;
the one-time $10 purchase is what makes this option usable.
Nothing is charged for a free model after that.

Create an API key, keep it in a Secret, and pick a model from the
[models page](https://openrouter.ai/models), filtered by free and by
tool support, that is free and supports tool calling:

```sh
kubectl -n sidekick create secret generic sidekick-openrouter --from-literal=apiKey='<OpenRouter API key>'
```

```yaml
model:
  connectors:
    openrouter:
      provider: openai
      model: <a model ID ending in :free>
      baseURL: https://openrouter.ai/api/v1
      apiKeySecret: { name: sidekick-openrouter, key: apiKey }
      capabilities:
        toolCalling: true
        toolChoice: false
        contextTokens: 131072
        maxOutputTokens: 16384
  roles:
    investigate: openrouter
```

Sidekick does not know these models, so the connector needs the
`capabilities` block, with the context size and output limit of the model
you picked. Leave `toolChoice` off unless the model's page says it honours a
forced tool choice. Free models change and come and go; a model that stops
answering needs a new `model` line, not a new setup. Your alerts and
reports go to OpenRouter and to the company that hosts the model, and a
free model's host may keep them, so read the model's page before you send
anything private.

## 3. The Gemini API free tier

Google gives a free tier of the Gemini API to a key from a project without
billing. It needs only a key, and its models can be forced to call a tool, so
it is the quickest way to see a good investigation. It has two
costs:

- **Your data is used.** Google may use free-tier prompts and answers to
  improve its products, and people may read them. Use this setup with test
  data only: a demo cluster, sample alerts, or the
  [`sidekick test-alert`](webhooks.md#sending-a-test-alert) alert, and
  never with logs, traces or code of a real system.
- **The limits are low.** Free-tier rate limits are far below what a team's
  alerts need, change often and differ by model; see Google's
  [rate limits page](https://ai.google.dev/gemini-api/docs/rate-limits) for
  the current numbers. An investigation that hits one waits and retries,
  and fails when the retries run out.

Create a key in [Google AI Studio](https://aistudio.google.com/apikey) in a
project that has no billing account, and keep it in a Secret:

```sh
kubectl -n sidekick create secret generic sidekick-gemini --from-literal=apiKey='<Gemini API key>'
```

```yaml
model:
  connectors:
    gemini:
      provider: google
      model: gemini-3.8-flash
      apiKeySecret: { name: sidekick-gemini, key: apiKey }
      effort: medium
  roles:
    investigate: gemini
```

The free tier costs nothing, but the price table of Sidekick has the paid
prices of the Gemini API, so the cost shown for a step and counted in the
budgets is what the call would cost on a paid key. Set `model.prices` to
zero for the model if you want the budgets to count nothing.
[The `google` provider](configuration.md#the-google-provider) has the rest
of the keys.

## Published scores

Each setup runs the [public benchmark](benchmark.md) (`bench/scenarios/public`)
with the built-in strategies, so you can see how much quality you give up
to save cost. The first row is the default model of the quick start, run by
the project's benchmark workflow. The free setups run on a machine with a
GPU, once for each release.

Run the benchmark on your own setup with:

```sh
sidekick bench run --scenarios bench/scenarios/public --config sidekick.yaml --repeat 3 --out report.json
sidekick bench compare report.json other-report.json
```

| Setup | Model | Served by | Verdict accuracy | False positive precision | Root cause match | Mean cost of a run | Sidekick version |
|---|---|---|---|---|---|---|---|
| Default | `claude-opus-5-5` | Anthropic API | not yet published | not yet published | not yet published | not yet published | |
| 1, local | `Qwen/Qwen3.8-27B` | vLLM | not yet published | not yet published | not yet published | none | |
| 1, local | `qwen3.8:27b` | Ollama | not yet published | not yet published | not yet published | none | |
| 1, local | `google/gemma-4-31B-it` | vLLM | not yet published | not yet published | not yet published | none | |
| 2, OpenRouter | a `:free` model | OpenRouter | not yet published | not yet published | not yet published | none | |
| 3, Gemini | `gemini-3.8-flash` | Gemini API, free tier | not yet published | not yet published | not yet published | none | |

The scores come from `report.json` of each run, which also records the
model settings and the prompt and strategy versions, so two rows can be
compared with `sidekick bench compare`. A score with a 95% interval that
overlaps another's may differ by chance only.
