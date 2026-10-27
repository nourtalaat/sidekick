**🔕 False alarm** · High confidence · Sidekick investigated checkout\-api p99 latency above 2s

A deploy at 10\:02 doubled the database calls per checkout.

**Why it fired**

A nightly batch job raises p99 for two minutes.

**How to stop it**

- Fire only after 10 minutes above 2s.

**Findings**

- Database calls per request went from 4 to 9 at 10\:02. · [DB calls per request](https://signoz.example.com/q/1)
- _Hypothesis, no evidence yet\:_ The cache was removed on purpose.

**Proposed fixes**

1. **Roll back deploy 4f2a**\: Restores the cache within minutes.

_Checked signoz, github_