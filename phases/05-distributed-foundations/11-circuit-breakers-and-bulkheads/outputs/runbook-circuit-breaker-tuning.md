---
name: runbook-circuit-breaker-tuning
description: One-page runbook for choosing, reviewing, and alerting on circuit-breaker and bulkhead parameters for a downstream dependency
phase: 5
lesson: 11
tags: [resilience, circuit-breaker, bulkhead, operations, design-review]
---

# Circuit Breaker and Bulkhead Tuning Runbook

Fill one copy per downstream dependency (card processor, ledger, FX rate service, KYC vendor). Attach it to the dependency's design document and revisit it after every incident that involved the dependency.

## 1. The dependency

- Name and owner of the dependency:
- What we call it for, and what happens to the customer if the call is rejected (the fallback):
- Normal traffic: ______ calls/s at peak, ______ calls/s at trough
- Normal latency: p50 ______ ms, p99 ______ ms; per-call timeout ______ ms (Lesson 08)
- Observed outage durations in the last year (from the vendor's status page or our incidents): shortest ______, typical ______, longest ______

## 2. The four breaker numbers

| Parameter | Value | The question that decides it |
|-----------|-------|------------------------------|
| `windowMs` / `bucketMs` | | How many seconds of evidence is "recent"? At trough traffic, does the window still hold at least `minimumCalls` calls? If not, lengthen the window or lower `minimumCalls`. |
| `minimumCalls` | | How many real failures do we need before we refuse to serve customers? With a 50% threshold, half this number. Below 10 the breaker trips on noise; above 100 at trough traffic it may never trip. |
| `failureRateThresholdPercent` | | What error rate is unambiguously an outage rather than a bad tail? Compare with the dependency's normal error rate under load; set the threshold well above it. |
| `openDurationMs` | | How long does a typical outage last? Too short probes a dead host continuously (each probe burns a timeout); too long rejects good traffic after recovery. Start near the *shortest* observed outage. |
| `halfOpenPermits` | | How many successful calls convince us? One is fragile (a single lucky call closes the breaker); more than the bulkhead size is impossible. |

- [ ] The numbers are written in the operator's vocabulary in the config, not as magic constants in code.
- [ ] A test drives the breaker with a fake clock through CLOSED -> OPEN -> HALF_OPEN -> CLOSED and asserts the transition times.

## 3. What counts as a failure

- [ ] Timeouts and connection errors: yes.
- [ ] 5xx responses: yes.
- [ ] 4xx responses: no (they are about our request, not the dependency's health), except 429 if the vendor documents it as overload.
- [ ] Business declines (insufficient funds, card declined): never. A declined card is a successful call.
- [ ] Slow successes above ______ ms: decide, and write the decision here.

## 4. The bulkhead

| Parameter | Value | The question that decides it |
|-----------|-------|------------------------------|
| `maxConcurrent` | | Little's law: peak calls/s times timeout in seconds is the worst-case in-flight count while the dependency is dead. What fraction of our worker pool may this dependency hold at that moment? |
| Wait before rejecting | | Zero unless a short queue measurably reduces rejections under normal spikes. Any wait is capacity lent to the dependency. |

- [ ] A bulkhead rejection is not recorded in the breaker's window and does not consume a half-open probe.
- [ ] A bulkhead rejection is not retried by the retry policy (Lesson 10).
- [ ] The bulkhead is per dependency, not shared across dependencies.

## 5. Observability

Emit and alert on:

- [ ] Every state transition, with the time and the failure rate at the moment of the trip. Page on CLOSED -> OPEN for a payments dependency.
- [ ] Count of `REJECTED_OPEN` and `REJECTED_BULKHEAD` per minute, as separate series. A rising bulkhead count with a CLOSED breaker is a capacity problem on our side.
- [ ] Time spent in OPEN per incident. Compare with the dependency's actual outage window to tune `openDurationMs`.
- [ ] The failure rate in the window as a gauge, so an operator can see the breaker approaching its threshold before it trips.

## 6. After an incident

Answer these in the postmortem:

1. When did the dependency actually fail, and when did the breaker open? The gap is the cost of `minimumCalls` and the window; was it acceptable?
2. When did the dependency actually recover, and when did the breaker close? The gap is the cost of `openDurationMs`; how many good calls were rejected?
3. Did any probe batch hit a dependency that was still dead? How many times? Each batch cost `halfOpenPermits` timeouts.
4. Did the bulkhead reject calls while the breaker was CLOSED? If so, was the dependency slow rather than failing, and should slow calls count as failures?
5. Did the fallback work? Were rejected customers told something true?
