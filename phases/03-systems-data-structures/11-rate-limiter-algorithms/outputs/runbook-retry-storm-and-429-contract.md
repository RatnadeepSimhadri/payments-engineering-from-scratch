---
name: runbook-retry-storm-and-429-contract
description: The server-side 429 and Retry-After contract for a rate-limited payment endpoint, plus the runbook for recognising and stopping a retry storm
phase: 3
lesson: 11
tags: [rate-limiting, 429, retry-after, retry-storm, runbook, payments]
---

# Part 1: The 429 Contract

Write this down for every rate-limited endpoint before it ships. It is what the limiter promises clients and what operations will rely on at 14:02 on a Friday.

## What the endpoint promises

- [ ] **Scope of the limit** is stated: per API key, per merchant, per IP, per endpoint, or global, and which combination.
- [ ] **The algorithm** is named, with its parameters (token bucket: capacity and refill; windows: limit and width) and the one-second ceiling that follows from them (token bucket: capacity + rate; fixed window: 2x limit at a boundary; sliding: limit).
- [ ] **The status code is 429** (RFC 6585) for a client-caused limit. 503 is reserved for the server being unable to serve anyone.
- [ ] **`Retry-After` is always present on a 429**, in delta-seconds, rounded up, never zero. Its value is computed by the algorithm (time to next token; time to window boundary; time for the oldest log entry to age out), not a constant.
- [ ] **Allowance headers** (`RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset`, per the IETF draft) are sent on every response so a well-behaved client can slow down before being denied.
- [ ] **The response body** names the limit that was hit and the scope, so an integrator can tell a per-merchant limit from a global one.

## What the endpoint requires of clients (publish this)

- [ ] Honour `Retry-After` as a floor: never retry sooner.
- [ ] Add jitter: retry at `Retry-After + random(0, Retry-After)`, or use full-jitter exponential backoff with a cap.
- [ ] Send an `Idempotency-Key` on every mutating request so a retry is a replay, never a second charge.
- [ ] Do not retry 4xx other than 429 and 408; do retry 429, 503, and network timeouts, with a maximum attempt count.
- [ ] Mark retries (`X-Retry-Attempt: n` or equivalent) so the server can prioritise first attempts under load.

## Server-side implementation checks

- [ ] The limiter reads an injected clock; tests advance it and assert the exact admit/deny boundary (499 ms denies, 500 ms admits).
- [ ] State per client is bounded (two or three numbers for bucket and counter algorithms); if a sliding log is used, the per-client memory at the limit is written down and multiplied by the client count.
- [ ] Multiple instances: either the limit is per instance and documented as such (effective limit = per-instance x instances), or the state is shared (Phase 9, Lesson 05).
- [ ] Idle clients' state is evicted (LRU) and the recreated state starts full; the PR says why that is acceptable.
- [ ] The limiter is applied before any expensive work: before authentication's database call if the key is in the request, and always before the downstream call.

## Metrics and alarms

- [ ] `rate_limited_total{scope, algorithm}`: 429s issued.
- [ ] `rate_limit_utilisation{client}`: tokens remaining or weighted count, sampled, so a client approaching its limit is visible before it hits it.
- [ ] `retry_attempt_ratio`: share of inbound requests marked as retries. A sustained rise is the storm's leading indicator.
- [ ] `downstream_inflight`: concurrent requests to the acquirer or ledger. The storm's damage shows here first.
- [ ] Alarm: `retry_attempt_ratio` above 25% for 60 s, or 429 rate above 10x its weekly baseline.

# Part 2: Retry Storm Runbook

## Recognise it

A retry storm has all three of these at once; a legitimate traffic spike has at most the first:

1. Inbound request rate several times normal.
2. Retry-marked requests (or requests with a repeated `Idempotency-Key`) make up most of the increase.
3. A downstream dependency's error or timeout rate rose *before* the inbound rate did.

Confirm with: top clients by request rate over the last five minutes versus the previous hour; the share of those requests that carry a previously seen idempotency key; the downstream dependency's error timeline.

## Stop it

Work top-down; each step is reversible and takes effect within seconds.

1. **Shed retries first.** Lower the per-client limit for requests marked as retries (or carrying a seen idempotency key) to a fraction of the normal limit. First attempts keep their allowance. This alone usually halves downstream load.
2. **Tighten the loudest clients.** Apply a per-client override to the top offenders identified above, with a `Retry-After` of at least 30 s. Record the override with an expiry so it is not forgotten.
3. **Tighten globally if downstream is still saturated.** Reduce the global token bucket's refill to what the degraded dependency can actually serve (its current successful throughput). A 429 now is cheaper than a timeout in four seconds.
4. **Do not raise timeouts** to "give requests a chance". Longer timeouts hold connections longer and make the storm worse.
5. **Confirm the replay path.** Spot-check that retries with a seen idempotency key are returning the stored response, not re-executing. If they are re-executing, the idempotency store is the incident, not the limiter.

## Verify it has stopped

- [ ] Downstream in-flight count and error rate back to baseline.
- [ ] `retry_attempt_ratio` below 10% and falling.
- [ ] 429 rate falling, not flat: a flat 429 rate means clients are retrying exactly at `Retry-After` in lockstep; contact the top offenders about jitter.
- [ ] Remove per-client overrides (step 2) and the global tightening (step 3) one at a time, watching downstream after each.

## Afterwards

- [ ] For each top offender, confirm which retry rule they violated (no backoff, no jitter, ignored `Retry-After`, no idempotency key) and send them the client requirements from Part 1.
- [ ] Replay the storm's inbound trace (timestamps per client) through the lesson's limiters offline. Record what each algorithm would have admitted and whether the chosen one needs different parameters.
- [ ] If the trigger was a downstream degradation, confirm the circuit breaker or timeout budget for that dependency (Phase 9, Lesson 13) would have opened before the storm formed.
