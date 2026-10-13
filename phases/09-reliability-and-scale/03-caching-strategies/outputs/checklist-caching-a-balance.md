---
name: checklist-caching-a-balance
description: Review checklist for any pull request that puts a cache in front of money or any other state that decides what the system does next
phase: 9
lesson: 3
tags: [caching, staleness, review, consistency]
---

# Caching a Balance: Review Checklist

Use on any change that adds or modifies a cache in front of balances, limits, idempotency records, account status, or any other value an authorization decision reads. One unchecked box is a reason to request changes.

## Strategy

- [ ] The PR names the strategy (cache-aside, read-through, write-through, write-behind) and says why. "Added Redis" is not a strategy.
- [ ] If the strategy is write-behind, the cached state is derived data that can be recomputed from a durable source, and the PR says what recomputes it after a cache crash. Write-behind in front of a ledger, a balance, or an idempotency store is rejected.
- [ ] Reads that drive a money decision (authorize, approve, settle) either bypass the cache or read a value the PR can prove is not stale for writers it controls.

## Writers

- [ ] Every process that writes the underlying table is listed: the API, batch jobs, migrations, support tooling, the reconciliation job, the ops runbook's manual SQL.
- [ ] Each listed writer either goes through the cache's write path or is covered by change data capture that invalidates the cache. "The batch job runs at night when traffic is low" is not coverage.
- [ ] Invalidation happens after the store write commits, never before.
- [ ] There is a TTL as a backstop even when invalidation is in place, and the PR states the maximum staleness it implies in words a risk reviewer can read ("a balance may be up to 5 seconds old").

## Keys and values

- [ ] The key format is defined in one place and includes a version segment so a schema change can be rolled out without serving old shapes.
- [ ] A `null` or not-found from the store is either not cached or cached as an explicit tombstone with its own, shorter TTL and a documented consequence (a newly created account is invisible for that long).
- [ ] Money in the cache is serialized as integer minor units plus currency (Phase 10, Lesson 01), never as a float.
- [ ] Values are immutable after read; nothing mutates a cached object in place.

## Measurement

- [ ] The test suite includes an oracle: a test that compares what the cache returns with what the store holds after a write from each writer in the Writers section, and asserts zero divergence for the writers that use the write path.
- [ ] Hit rate, miss rate, and store QPS are exported as metrics (Lesson 06) per cache, not per process.
- [ ] A staleness metric exists in production for at least a sample of reads: compare the cached value with the store on 1 in N reads and count disagreements.
- [ ] A load test or seeded simulation shows the store can survive the cache being empty (cold start, cluster restart, mass expiry). If it cannot, Lesson 04 is required reading before merge.

## Operations

- [ ] There is a documented, tested way to invalidate one key and all keys for one account from an ops tool.
- [ ] Flushing the whole cache is possible and the PR states the store load it causes.
- [ ] The cache's memory bound and eviction policy are set explicitly; an unbounded TTL cache is a memory leak with a timer.
