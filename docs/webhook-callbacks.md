# Webhook callbacks

The `webhook` output posts the final result of each investigation, and each
change of a watcher's state, as JSON to a URL you choose, signed with a
shared secret, so another system can act on verdicts: close a ticket, page
someone, record the result.

```yaml
outputs:
  callback:
    type: webhook
    url: https://hooks.internal.example.com/sidekick     # required
    hmacSecret: { name: sidekick-hooks, key: callback }  # required
```

| Key | Required | Meaning |
|---|---|---|
| `url` | yes | Where callbacks go. It must be https, except for `localhost` or a loopback address such as `127.0.0.1`. It must not hold a user name or password. |
| `hmacSecret` | yes | The secret callbacks are signed with. The receiver needs the same secret. |

List the output in the routes whose results should be sent.

## What is sent

One `POST` per investigation, when its result is final, with
`Content-Type: application/json`. The result is sent again only when its
report changes, not when something else about the investigation does,
such as a watcher or a rating:

```json
{
  "event": "investigation.final",
  "deliveryId": "0199c9d2-7f00-7000-8000-000000000001:investigation.final:5",
  "investigationId": "0199c9d2-7f00-7000-8000-000000000001",
  "seq": 5,
  "url": "https://sidekick.example.com/investigations/0199c9d2-7f00-7000-8000-000000000001",
  "status": "completed",
  "alert": {
    "intake": "alertmanager",
    "kind": "alert",
    "alertName": "CheckoutLatencyHigh",
    "title": "checkout-api p99 latency above 2s",
    "service": "checkout-api",
    "severity": "critical",
    "labels": { "env": "prod", "service": "checkout-api" },
    "fingerprint": "3f2a9c",
    "startsAt": "2026-10-26T10:02:00Z",
    "receivedAt": "2026-10-26T10:04:00Z",
    "links": [{ "title": "Source", "url": "https://prometheus.example.com/graph" }]
  },
  "events": 2,
  "report": {
    "verdict": "true_positive",
    "confidence": "high",
    "reason": "A deploy at 10:02 doubled the database calls per checkout.",
    "cause": "Deploy 4f2a removed a query cache.",
    "impact": "Checkout p99 is 2.8s.",
    "findings": [{ "id": "f1", "claim": "Calls doubled.", "evidence": [{ "kind": "query", "title": "DB calls", "url": "https://signoz.example.com/q/1" }] }],
    "fixes": [{ "id": "x1", "title": "Roll back deploy 4f2a", "detail": "Restores the cache." }],
    "checked": [{ "source": "signoz", "status": "ok", "toolCalls": 3 }],
    "stopReason": "submitted"
  }
}
```

- `status` is `completed` for a run that ended with a verdict and `failed`
  for one that could not finish. A failed run may have no `report`; when it
  has one, `report.stopDetail` says why it stopped.
- `alert` is the event that opened the investigation's group, after
  redaction. Its free text and raw payload are not sent.
- `url` is set only when the web UI is on.
- `report` has the fields described in
  [How investigations run](investigations.md#the-report). Its text comes from the
  model and from the alert's sender: treat it as untrusted, and escape it
  before you show it. The JSON already escapes `<`, `>` and `&`.

## Watcher events

When a watcher on the investigation's group starts or reaches a final
state, a callback with the same envelope reports it. `event` is
`watcher.started`, `watcher.recovered`, `watcher.timed_out` or
`watcher.stopped`; `watcher` holds the watcher's state; there is no
`status` or `report`:

```json
{
  "event": "watcher.recovered",
  "deliveryId": "0199c9d2-7f00-7000-8000-000000000001:watcher.recovered:6",
  "investigationId": "0199c9d2-7f00-7000-8000-000000000001",
  "seq": 6,
  "url": "https://sidekick.example.com/investigations/0199c9d2-7f00-7000-8000-000000000001",
  "alert": { "intake": "alertmanager", "kind": "alert", "alertName": "CheckoutLatencyHigh" },
  "events": 2,
  "watcher": {
    "id": "0199c9d2-8a00-7000-8000-000000000002",
    "name": "checkout-health",
    "checkType": "http",
    "status": "recovered",
    "passesInARow": 2,
    "passesToRecover": 2,
    "intervalSeconds": 30,
    "checks": 9,
    "lastCheckAt": "2026-10-26T10:10:00Z",
    "lastResult": "HTTP 200",
    "startedAt": "2026-10-26T10:06:00Z",
    "endedAt": "2026-10-26T10:10:00Z"
  }
}
```

- `watcher.stopped` has a `stopReason`: `group_resolved`, `responder` or
  `config_changed`. A watcher a responder stopped in Slack also has
  `stoppedBy`, with the responder's `slackUserId`.
- `intervalSeconds` is the time between two checks.
- `lastResult` is a short, redacted summary of the last check, such as
  `HTTP 503`.
- `seq` grows with every callback of one investigation, the final result
  included, so a receiver can tell which came last.

## Headers

| Header | Value |
|---|---|
| `X-Sidekick-Event` | The `event` of the body, such as `investigation.final`. |
| `X-Sidekick-Delivery` | The `deliveryId` of the body. Every attempt of one callback has the same one, so a receiver can drop repeats. |
| `X-Sidekick-Timestamp` | When the attempt was signed, in Unix seconds. |
| `X-Sidekick-Signature` | `sha256=` and the lower-case hex HMAC-SHA256, keyed with the secret, of the timestamp, a `.`, and the raw body. |

This is the scheme Sidekick's own webhook intake checks (see
[Sending alerts to Sidekick](webhooks.md)), so two Sidekick installations
can send results to each other.

## Checking a callback

Compute the HMAC over the raw body as it arrived, before parsing it,
compare it in constant time, and refuse a timestamp more than a few minutes
from your clock, so an old callback cannot be sent again later:

```go
func verify(r *http.Request, body []byte, secret []byte) bool {
	ts := r.Header.Get("X-Sidekick-Timestamp")

	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(sec, 0)).Abs() > 5*time.Minute {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(want), []byte(r.Header.Get("X-Sidekick-Signature")))
}
```

The secret itself is never sent.

## Answers, retries and redirects

- Any 2xx answer counts as delivered. Anything else, or no answer, makes
  the publish job try again with backoff for about half an hour, each try
  signed with a fresh timestamp. A 429 with a short `Retry-After` is waited
  out first.
- A redirect is followed only when it is a 307 or 308, which keep the
  method and body, to the same scheme, host and port, at most 3 times.
  Every other redirect fails the delivery, so the signed body never goes
  to another host and a `POST` is never turned into a `GET`. Point `url` at
  the final address.
- Failed deliveries are counted in `sidekick_output_api_errors_total` with
  `type="webhook"`; see [Running Sidekick](running.md#metrics).
