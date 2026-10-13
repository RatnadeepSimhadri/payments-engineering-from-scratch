---
name: runbook-resharding-with-a-hash-ring
description: Step-by-step runbook for adding or removing a shard behind a consistent hash ring without freezing writes
phase: 3
lesson: 6
tags: [consistent-hashing, sharding, migration, ledger, runbook]
---

# Resharding Behind a Hash Ring

For adding or removing one storage shard (database, cache node, partition owner) whose keys are routed by a consistent hash ring. The ring tells you which keys move; this runbook is everything the ring does not do. Each step has a check that must pass before the next step starts.

## 0. Preconditions

- [ ] Every client of the shards routes through the same ring library, same hash function, same virtual-node count. A cross-language or cross-service test asserts identical owners for a fixed key list.
- [ ] The ring membership is versioned (`ring_version` integer) and distributed from one source (config service, coordination store). Clients log the version they route with.
- [ ] Each shard rejects writes for keys it does not own under the version it currently holds, or at least logs them. This is the backstop against split-brain routing.
- [ ] Per-key or per-range ownership state can be stored: `OWNED_BY_OLD`, `COPYING`, `OWNED_BY_NEW`.

## 1. Compute the moved set

1. Build `ringBefore` from version `v` and `ringAfter` from `v+1` (the new membership) offline.
2. Enumerate keys (or key ranges) and record `(key, ownerBefore, ownerAfter)` where they differ.
3. **Check:** for an add, every `ownerAfter` in the moved set is the new node; for a remove, every `ownerBefore` is the removed node. Anything else means the two rings were not built from the same hash or virtual-node settings. Stop.
4. **Check:** the moved fraction is close to `1/(N+1)` for an add or the removed node's share for a remove. A fraction near 1 means a mod-N style rule has crept in somewhere.
5. Record the moved count and estimated bytes; this is the migration's size.

## 2. Prepare the new shard (add only)

- [ ] Schema, indexes, and constraints identical to the existing shards; verified by diffing `pg_dump --schema-only` or the equivalent.
- [ ] Capacity for the moved set plus growth headroom.
- [ ] The new shard rejects writes for keys it does not own under `v+1`.

## 3. Dual-read window

1. Publish ring version `v+1` to all clients with the rule: **write to `ownerBefore`, read from `ownerBefore` first and `ownerAfter` second** for keys in the moved set (or for all keys, if the moved set is not cheap to test; a Bloom filter over the moved set makes it cheap).
2. **Check:** every client logs `ring_version = v+1` within the rollout deadline. A client still on `v` is routing writes to the old owner anyway, so this window is safe, but the window cannot close until it reports `v+1`.

## 4. Copy, key by key or range by range

For each key or range in the moved set:

1. Set state `COPYING`.
2. Copy the data from `ownerBefore` to `ownerAfter`. For a ledger account: balance row, posting history, and any open holds, inside one transaction on the destination.
3. Compare a checksum (row count plus a hash of ordered postings) between source and destination.
4. Set state `OWNED_BY_NEW` and switch the client write rule for that key to `ownerAfter`. From here, reads go to `ownerAfter` first.
5. **Check:** a write that arrives at `ownerBefore` for a key in `OWNED_BY_NEW` is rejected and retried against the ring. Count these; they should be rare and fall to zero as clients refresh.

Batch size and pacing: start small (100 accounts), watch replication lag and p99 latency on both shards, then grow. Never exceed the rate at which step 3's checksum can keep up.

## 5. Verify

- [ ] Every key in the moved set is `OWNED_BY_NEW`.
- [ ] A full-ring scan finds no key whose stored location differs from `ringAfter.lookup(key)`.
- [ ] For a ledger: the sum of balances across all shards before and after is identical, and the per-account trial balance holds on the destination shard (Phase 10, Lesson 03).
- [ ] The "wrote to the wrong shard" counter has been zero for at least one full client-refresh interval.

## 6. Close out

1. Publish the rule change: reads and writes go to `ringAfter` only; the dual-read path is removed.
2. Delete the moved data from `ownerBefore` (add) or decommission the shard (remove) only after step 5 has passed and a backup of the source exists.
3. Record: moved count, duration, peak lag, number of wrong-shard writes, ring version. Compare with the estimate from step 1 for the next time.

## Rollback

At any point before step 6, roll back by publishing ring version `v` with the dual-read rule inverted (write to `ownerBefore`, read both). Keys already `OWNED_BY_NEW` are copied back by running step 4 with the roles swapped. Because monotonicity guarantees only the moved set was ever affected, rollback touches the same bounded key set and nothing else.

## What this runbook assumes the ring provides

- Monotonicity: only the moved set changes owner. If a test of `fractionMoved` shows otherwise, the ring implementation is wrong, not the runbook.
- Deterministic `lookup` across every client for a given version.
- A preference list (`lookupN`) if replicas are involved; replicate the copy to each node in the new list before marking `OWNED_BY_NEW`.
