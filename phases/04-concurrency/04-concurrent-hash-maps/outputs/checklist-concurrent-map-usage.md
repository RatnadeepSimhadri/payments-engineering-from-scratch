---
name: checklist-concurrent-map-usage
description: Review checklist for code that reads or writes a concurrent map (ConcurrentHashMap, sync.Map, a striped cache, an idempotency store)
phase: 4
lesson: 4
tags: [concurrency, hash-map, check-then-act, review]
---

# Concurrent Map Usage Checklist

Use on any pull request that touches a `ConcurrentHashMap`, `sync.Map`, a striped or sharded in-memory store, or any map shared between threads or request handlers. "The map is thread-safe" closes none of these boxes.

## Check-then-act

- [ ] Grep for `get(` followed by `put(` on the same key, `containsKey(` followed by `put(`, and `if (map.get(k) == null)`. Each one is a race unless the whole sequence is under a lock the reviewer can point at.
- [ ] Every "create if missing" uses `putIfAbsent`, `computeIfAbsent`, `compute`, or the store's equivalent (`SET NX`, `INSERT ... ON CONFLICT DO NOTHING`, `dict.setdefault`).
- [ ] Every "update based on current value" uses `compute` or `merge`, never `put(k, get(k) + delta)`.
- [ ] Every "delete if equals" uses `remove(key, expectedValue)` or `compute` returning `null`, not `get` then `remove`.

## Functions passed to `compute` and friends

- [ ] The function is short, allocation-light, and has no I/O: it runs while the key's bin or stripe is locked.
- [ ] The function does not call back into the same map (deadlock on a reentrant lock, or corruption of another bin).
- [ ] The function is idempotent or pure where the implementation may retry it (`AtomicReference.updateAndGet`, some lock-free maps).
- [ ] The function never returns a value that encodes "absent" (for example `null` in a map that forbids nulls) by accident.

## Reading the map

- [ ] `size()` and `isEmpty()` are not used for a capacity, quota, or billing decision; they are estimates while writers run.
- [ ] Iteration is understood to be weakly consistent: entries added or removed during the walk may or may not appear. No code assumes the iteration is a snapshot.
- [ ] `null` from `get` is treated as "absent" and the map forbids `null` values, so no second `containsKey` is needed.

## Multi-key operations

- [ ] Any operation that must change two keys together (a transfer, a swap, a rename) is under an external lock covering both, ordered by a stable key order, or it is routed to a single owner (actor, partition, database transaction). A concurrent map alone cannot do it.
- [ ] Invariants that span keys (sum of balances equals a total) are checked only under that external lock or in a quiesced state.

## Hashing and distribution

- [ ] Keys have a well-distributed `hashCode`; short keys that differ in one character are mixed before selecting a stripe or shard.
- [ ] If the map is sharded across processes, the hash is stable across runtimes and versions (not Python's salted `hash()`, not `Object.hashCode()` identity).

## Memory and lifetime

- [ ] The map has a bound: a maximum size, an expiry, or an eviction policy. An idempotency store without a TTL is a slow memory leak.
- [ ] Expiry or eviction runs without blocking readers, and its clock is injectable so it can be tested.

## Tests

- [ ] There is a barrier-driven test that forces the check-then-act interleaving and asserts exactly one creator for `putIfAbsent`.
- [ ] There is a phased concurrent test with barriers whose assertions are end-state facts that every legal schedule must produce (sums, counts, presence), not timings.
- [ ] If a custom map was written instead of the library one, the PR explains why `ConcurrentHashMap` or `sync.Map` was not enough, and the reviewer agrees.
