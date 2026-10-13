---
name: checklist-money-movement-observability
description: Review checklist for the metrics, SLOs, and alert rules of any service that moves money
phase: 14
lesson: 6
tags: [observability, slo, alerting, payments, monitoring]
---

# Money Movement Observability Checklist

Run this against a payment service's dashboards and alert rules. Every unchecked box is a way a customer can find a problem before you do.

## Per-payment state

- [ ] Every payment records `state` and `state_entered_at`, updated on every transition, indexed together.
- [ ] Every non-terminal state has a written age limit derived from the business process (auth expiry, capture timeout, settlement deadline), not from latency percentiles.
- [ ] The stuck query uses a strict comparison and a documented boundary.
- [ ] Stuck alerts page on the stuck **amount**, not the count; small amounts ticket.
- [ ] Stuck alerts name the oldest payment so on-call can start from a concrete id.

## Exposure

- [ ] In-flight exposure is exported per state and per currency; currencies are never summed together.
- [ ] A state-by-age-band histogram is on the first screen of the dashboard.
- [ ] Exposure resets visibly after each settlement run; a run that does not reset it is investigated.

## Authorization

- [ ] Auth rate is computed per processor (and per card brand where volume allows) over 5m, 15m, and 1h.
- [ ] Alerts compare a processor with the fleet over the same window, not with a fixed target.
- [ ] Rates below a minimum sample size are not computed and never alert.
- [ ] Rates and thresholds are integers (basis points), so replays and tests are exact.

## SLOs and burn rate

- [ ] The availability SLI counts issuer declines as **good** events; only timeouts, connection failures, and processor 5xx are bad.
- [ ] Burn-rate alerts use the long/short window pairs (1h/5m at 14.4, 6h/30m at 6 page; 1d/2h at 3, 3d/6h at 1 ticket) or a documented variant.
- [ ] Long-window rules are disarmed until the metric has full coverage (after a deploy, a new processor, a new region).
- [ ] A test proves an ended spike stops paging within the short window.

## Reconciliation backstop

- [ ] Break rate over recent runs is a metric with a ticket threshold.
- [ ] Timeouts and unknown outcomes are reconciled against the processor report the same day.
- [ ] A missing reconciliation run alerts (silence is not success).

## Testability

- [ ] Every rule takes time from an injected clock; no rule reads the wall clock.
- [ ] There is a replayable scenario test that injects a lost webhook and a processor outage and asserts which alerts fire and when.
