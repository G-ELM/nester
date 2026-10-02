# costmonitor

Call-volume budgets and alerts for the things Nester pays for on a
per-request or per-usage basis: Soroban RPC, Horizon, and third-party APIs
(DeFiLlama, Paystack, Flutterwave, …). The goal is to catch unexpected
mainnet-scale volume growth from inside the app — before it shows up as a
surprise on an invoice — not to replace a cloud bill.

## How it works

- `Tracker` counts outbound calls per `(category, provider)` per UTC day in
  Redis (`IncrBy` + `Expire`). With no `REDIS_ADDR` configured, every method
  is a no-op — cost monitoring is simply off, the same "unconfigured means
  disabled" convention used elsewhere in this app (see `internal/cache`).
- `WrapTransport`/`WrapTransportFunc` turn an existing `http.Client` into a
  tracked one by replacing its `Transport` — no changes needed at that
  client's call sites. Recording is fire-and-forget
  (`Tracker.RecordCallAsync`): a slow or unavailable Redis never adds
  latency to, or fails, the request being tracked. This matters most for
  `stellar.ContractInvoker` (wired via `WithUsageTracking`) — mainnet
  transaction submission must never gain a new failure mode from a
  monitoring feature.
- `BudgetChecker` compares today's usage against configured `Budget`s and
  fires an `Alerter.Alert` at most once per `(category, provider, severity)`
  per day when usage crosses `WarnThresholdPct` (warning) or the limit
  itself (critical).
- `Job` runs `BudgetChecker.Check` on a ticker, gated to a single leader
  replica via `LeaderChecker` (same pattern as every other alert-sending
  scheduler job — see `scheduler.APYDeviationJob`).
- `WebhookAlerter` posts a Slack-compatible `{"text": "..."}` payload —
  works with Slack incoming webhooks and most "Slack-compatible" ops
  tooling with no per-provider config.

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `COST_BUDGETS` | unset | `category:provider:dailyLimit` entries, comma-separated. See below. |
| `COST_MONITOR_ENABLED` | `true` iff `COST_BUDGETS` is set | Force on/off. |
| `COST_MONITOR_INTERVAL_MINUTES` | `15` | How often `Job` checks budgets. |
| `COST_ALERT_WARN_THRESHOLD_PCT` | `80` | % of a budget that triggers a warning alert. |
| `COST_ALERT_WEBHOOK_URL` | unset | Where `WebhookAlerter` posts. Empty = alerts are logged only. |
| `REDIS_ADDR` | unset | Required for tracking/alerting to do anything at all. |

`COST_BUDGETS` example — pick each `dailyLimit` from the provider's own
pricing (requests or compute units per dollar) so "usage crossed the
budget" tracks "spend crossed the budget you set":

```
COST_BUDGETS=stellar_rpc:soroban_rpc:500000,stellar_rpc:horizon:500000,third_party_api:defillama:100000,third_party_api:paystack:20000
```

## Currently wired

- `stellar.ContractInvoker` (Soroban RPC + Horizon), via
  `WithUsageTracking` — see `cmd/api/main.go`'s `chainInvoker` block.

## Extending tracking to another client

Any client built on `*http.Client` can be wired the same one-line way its
constructor allows setting `Transport`:

```go
client.Transport = costmonitor.WrapTransport(tracker, "third_party_api", "defillama", client.Transport)
```

`oracle.DefiLlamaProvider`, the Paystack/Flutterwave resolvers, and
`PrometheusClient` are good next candidates — each needs a small
`WithTransport`/`WithHTTPClient` setter added to its own constructor before
it can be wired in `main.go`, left out of this change to keep its blast
radius to the mainnet-critical RPC path.

## What this package does *not* cover: hosting costs

Compute/hosting spend (the box(es) the API and Postgres run on) isn't
request-shaped, so it isn't something an in-process call counter can track.
Set that up directly with whichever cloud/host this deploys to — all of the
below are console or one-time-CLI setups, not application code:

- **AWS**: [AWS Budgets](https://docs.aws.amazon.com/cost-management/latest/userguide/budgets-managing-costs.html) —
  a cost or usage budget with an SNS/email action. Scope one budget to the
  RDS/Postgres instance(s) and one to the compute (ECS/EKS/EC2) running this
  API, so a regression in either is attributable at a glance.
- **GCP**: [Budgets & budget alerts](https://cloud.google.com/billing/docs/how-to/budgets) —
  same shape, notifies via Pub/Sub or email.
- **Render / Railway / Fly.io** (or whichever PaaS hosts this): each has its
  own usage/spend-alert setting in its billing dashboard — there's no API
  for it, it's a checkbox plus a notification email/Slack webhook.

Point whichever of these applies at the same `COST_ALERT_WEBHOOK_URL`
channel this package posts to, so RPC/API and hosting alerts land in one
place.
