---
name: checklist-rate-limit-design
description: Design-review checklist for any new or changed rate limit on an endpoint that spans more than one node
phase: 9
lesson: 5
tags: [rate-limiting, reliability, redis, review]
---

# Rate Limit Design Review Checklist

Use before a limit ships and again when a 429 incident asks why the limit did not hold. Every unchecked box is a question for the author, not a reason to block.

## What the limit protects

- [ ] The limit names the resource it protects (processor connection, database pool, a tenant's fair share) and the number comes from that resource's measured capacity, not from a round figure.
- [ ] The cost of an over-admit is written down (processor-wide 429, pool exhaustion, cost overrun) and so is the cost of a false reject (lost checkout, support ticket, SLA breach).
- [ ] The key is explicit: per merchant, per API key, per IP, per card BIN, per endpoint. If several apply, each has its own limit and the order they are checked is documented.

## Which invariant

- [ ] The limit states whether bursts are allowed. "100 per second" with a token bucket admits up to 200 in the first second from idle; a sliding window admits exactly 100 in any second. The contract with the client or processor says which one it means.
- [ ] Capacity (burst) is justified separately from rate. A capacity equal to the rate is a default, not a decision.
- [ ] If the limit is advertised to clients, the advertised number is the one actually enforced across the whole cluster, not per node.

## Where the counter lives

- [ ] The design is one of: centralized atomic counter (exact, one round trip per request), local shares with periodic sync (zero latency, under-admits after traffic shifts), or sliding-window log/counter in a shared store (exact per window, memory proportional to the limit).
- [ ] If centralized: the read-modify-write is atomic (Lua script, `MULTI`/`EXEC`, or a single `INCR`), uses the store's clock, and sets a TTL so idle keys expire.
- [ ] If local with sync: the sync interval is chosen against the measured skew of traffic across nodes, and the maximum under-admission for that interval is stated.
- [ ] The store's added latency is measured at p99, not p50, and compared against the endpoint's latency SLO.
- [ ] One hot key's throughput ceiling (one store node's operation rate) is known and exceeds the worst expected load for that key.

## When the store is unavailable

- [ ] Fail-open, fail-closed, or fall-back-to-local is chosen per limit and written in the code next to the call, not left to the client library's default.
- [ ] The store timeout is shorter than the endpoint's latency budget, so a slow store cannot take the endpoint's SLO down with it.
- [ ] An alert fires when the limiter is in its fallback mode for more than a few seconds.

## Telling the client

- [ ] Rejects return `429 Too Many Requests` (RFC 6585) with `Retry-After`, and ideally `RateLimit-Limit` / `RateLimit-Remaining` / `RateLimit-Reset`.
- [ ] Documentation tells clients to back off with jitter on 429 and never to retry immediately.
- [ ] Rejected requests are not counted against the limit (a sliding log stores admitted timestamps only), so a client already over the limit can recover.

## Observability

- [ ] Admitted and rejected counts are metrics labelled by key class (not by individual key, which would explode cardinality), and the rejection ratio has a dashboard panel.
- [ ] Store round-trip latency and error rate for the limiter path have their own panel.
- [ ] A load test exists that drives N nodes at several times the limit with skewed traffic and asserts the admitted count per second, so the invariant is verified before production verifies it for you.
