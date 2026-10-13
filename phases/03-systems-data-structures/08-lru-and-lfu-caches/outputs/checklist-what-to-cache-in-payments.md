---
name: checklist-what-to-cache-in-payments
description: Review checklist for any change that adds or modifies a cache in a payment path, covering data classification, freshness, eviction policy, and metrics
phase: 3
lesson: 8
tags: [caching, lru, lfu, ttl, review, payments]
---

# What To Cache in a Payment Path

Use on every pull request that introduces a cache, changes its policy, or caches a new kind of data. One unchecked box is a reason to request changes.

## 1. Classify the data before anything else

For each cached type, the PR answers in writing:

- [ ] **What changes it, and how often?** (monthly table load; operator action; every posting)
- [ ] **What decision is made on the cached value?** (routing; fee computation; approve or decline)
- [ ] **What is the cost of acting on a value that changed since it was cached?** (wrong interchange category; wrong fee for one TTL; an overdraft)

Then apply the rule:

- [ ] Reference data that changes on a schedule or by operator action (BIN tables, merchant configuration, currency metadata, fee schedules) may be cached.
- [ ] Immutable data (idempotency key to response, settled posting by id) may be cached indefinitely.
- [ ] **Balances, available credit, holds, and velocity counters are not cached.** An approve or decline decision reads the ledger or a reservation, never a copy. No TTL makes this acceptable.
- [ ] FX rates are cached only with their timestamp and a hard maximum age, and the quote records the rate used.

## 2. Freshness

- [ ] Every entry has a TTL, or the PR states why the data is immutable.
- [ ] The TTL is justified against the reuse interval and the acceptable staleness, not picked as a round number.
- [ ] Changes to the source emit an invalidation (event, pub/sub, version bump) that every cache instance receives; the TTL is the backstop, not the mechanism.
- [ ] The cached value carries the source version (config version, table load id) and that version is logged with any money computation that used it.
- [ ] Expiry is lazy (checked on read) or eager (timer); the PR says which and acknowledges that lazy expiry lets dead entries occupy capacity.

## 3. Policy and capacity

- [ ] Capacity is sized from the measured working set, not from available memory.
- [ ] The policy is chosen from a replay of a representative trace, and the hit rates for LRU and LFU (or the library's policy) are in the PR.
- [ ] Known scans (batch jobs, table refreshes, reconciliation passes) either bypass the cache or the policy is scan resistant (segmented LRU, TinyLFU).
- [ ] If a frequency-based policy is used, it has aging or periodic reset so a moved hot set is learned.
- [ ] Entries are weighted by size if values vary by more than an order of magnitude.

## 4. Concurrency and process boundaries

- [ ] The cache is thread-safe (locking, striping, or a library that guarantees it). The lesson's from-scratch caches are not.
- [ ] Per-instance caches are acceptable for this data, or a shared cache is used and its network cost is measured against the lookup it replaces.
- [ ] Cold start is handled: a fresh instance under load does not stampede the source (Phase 9, Lesson 04).

## 5. Metrics

- [ ] Hit rate, exported and alarmed below the value the capacity planning assumed.
- [ ] Evictions per second (too small) and expirations per second (TTL too short), as separate series.
- [ ] Source load with and without the cache, so the cache's value is visible and its failure is detectable.
- [ ] Staleness: for config caches, the lag between a source change and the last instance observing it.

## 6. Tests present

- [ ] Eviction order test for the policy in use.
- [ ] TTL boundary test with an injected clock (alive at ttl - 1, dead at ttl).
- [ ] A test that an invalidation message drops the entry on every instance.
- [ ] For any value that influences an amount: a test that the computation records which cached version it used.
- [ ] No test, fixture, or helper anywhere in the PR reads a balance from a cache.
