---
name: runbook-hot-key-stampede
description: On-call runbook for recognizing a cache stampede in the first two minutes, stopping it, and choosing the permanent fix for the key
phase: 9
lesson: 4
tags: [caching, stampede, runbook, on-call, hot-key]
---

# Hot-Key Stampede Runbook

## Recognize it (first two minutes)

A stampede looks like a database incident with a cache-shaped cause. Check these in order; three or more means stampede.

- [ ] Database QPS or connection usage spikes in bursts with a period equal to a cache TTL (every 5 s, every 60 s), not a steady climb.
- [ ] The top query by count in the burst is one statement with one parameter value (one row, one key), repeated tens or hundreds of times within one query's latency.
- [ ] Cache miss rate on one key or key prefix spikes at the same period; overall miss rate may look normal because one key is a small fraction of traffic.
- [ ] Client p99 latency on the endpoint that reads that key jumps to roughly one database round trip at the same period.
- [ ] A recent deploy or config change added invalidate-on-write to a hot key, shortened a TTL, or slowed the backing query (new index missing, replica lag).

Write the key, the period, and the per-burst query count into the incident channel before changing anything.

## Stop it (next ten minutes)

Pick the first option that applies. Each one breaks one of the three stampede conditions.

1. **If the key is safe to serve stale for a short time** (configuration, fee schedule, rate table, dashboard aggregate): enable stale-while-revalidate for the key or prefix (Nginx `proxy_cache_use_stale updating`; application cache grace period) or lengthen the TTL by 10x. Readers stop waiting; the database sees one reload per period.
2. **If the key must not be served stale** (balance, limit, account status): enable a per-key reload lock (`SET lock:{key} {token} NX PX {reload_timeout}`) in the miss path, or turn on `proxy_cache_lock` at the edge. Confirm the lock has a TTL so a crashed holder cannot wedge the key.
3. **If the fix cannot be deployed in minutes**: shed the key. Return a cached-or-default response for that key from the application (feature flag), or rate-limit the endpoint for the one account (Lesson 05) so the database survives while the fix ships. Record the decision; this is a correctness trade made under pressure.
4. **If the database is already saturated**: kill the duplicate queries (`pg_terminate_backend` for the identical statements beyond the first) and apply option 1 or 2 before the next period arrives. You have one TTL to do it.

Do not "fix" the stampede by removing invalidation on write. That trades a database overload for stale balances (Lesson 03), which is a worse incident that is harder to detect.

## Confirm it stopped

- [ ] Peak concurrent executions of the statement per period is 1 (or one per process if coalescing is in-process only).
- [ ] Database QPS is flat across the TTL boundary.
- [ ] Client p99 on the endpoint returned to its pre-incident value.
- [ ] If stale-while-revalidate was enabled, the staleness metric for the key is bounded by the grace period you set and that number has been written into the incident channel.

## Permanent fix (before closing the incident)

Decide per key, using this table:

| Readers can tolerate | Choose | Why |
|----------------------|--------|-----|
| A bounded stale value, no waiting | Stale-while-revalidate with explicit grace, plus probabilistic early expiration | Readers never wait; store sees one load per period; staleness is on the entry |
| Waiting one reload, never stale | Single-flight in process, plus a shared lock if the fleet is large | One load per cliff; waiters pay one reload latency |
| Neither stale nor waiting | Do not cache in a TTL store; replicate the value (change data capture into a local read model) or read the primary | A cache cannot deliver both; pretending otherwise is how the incident happened |

Then:

- [ ] Add a test that reproduces the stampede with a simulator (Lesson 04 code) and asserts peak in-flight loads for the key is 1 after the fix.
- [ ] Add a per-key or per-prefix miss-rate metric and an alert on bursts aligned with the TTL.
- [ ] Add "concurrent identical statements" to the database dashboard; it is the most direct stampede signal.
- [ ] Review every other key with read rate x reload latency greater than 1 (Lesson 06 histograms give you both numbers) and apply the same decision.
- [ ] Postmortem (Lesson 12): the root cause is a design decision that was not made, not the TTL value.
