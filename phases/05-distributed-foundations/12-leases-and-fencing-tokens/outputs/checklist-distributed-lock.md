---
name: checklist-distributed-lock
description: Design-review checklist for any lock with an expiry that is held across processes or machines
phase: 5
lesson: 12
tags: [locks, leases, fencing, review, payouts]
---

# Distributed Lock Review Checklist

Use whenever a design says "only one worker at a time," "leader election," "take a lock in Redis/ZooKeeper/etcd," or "scheduled job with a lock." One unchecked box in the Correctness section is a reason to request changes.

## Classify first

- [ ] The design states whether this is an **efficiency** lock (failure wastes work; occasional double execution is harmless) or a **correctness** lock (failure corrupts data, moves money twice, or sends a message twice).
- [ ] For a correctness lock, every item in the Correctness section below is satisfied. For an efficiency lock, only the Lease section applies.

## Lease

- [ ] The lock has an expiry (ttl), and the ttl is justified against the work's p99 duration, not its median.
- [ ] Long-running holders renew the lease from a thread or task that the work itself cannot block.
- [ ] The client stops trusting the lease a safety margin before nominal expiry to absorb clock skew and transit time, and the margin is written down.
- [ ] Release only succeeds for the lease that is actually held (token or identity check); releasing someone else's lease is impossible.
- [ ] Renewing or releasing an expired lease is an error that the holder logs and handles, not a silent re-acquire.

## Correctness (fencing)

- [ ] Every grant carries a fencing token that is strictly larger than every previous grant for that lock: a persisted counter, a ZooKeeper zxid, an etcd revision, a Raft index. Random values and wall-clock timestamps are not acceptable.
- [ ] The token is passed to every write the holder makes to the protected resource.
- [ ] The resource stores the highest token accepted per key and rejects writes with a smaller token, atomically with the write (a conditional `UPDATE ... WHERE fence_token <= ?`, a versioned `setData`, an etcd transaction, or the `FencedStorage` pattern).
- [ ] The comparison rejects strictly smaller tokens and accepts equal ones, so the live holder's retries are not fenced.
- [ ] The holder treats a stale-token rejection as "re-acquire, re-read, recompute," never as "retry the same write."
- [ ] Irreversible external actions (bank transfers, emails, API calls) are either driven from a fenced write that records intent first, or carry an idempotency key, or both.

## Pauses and clocks

- [ ] The design has been walked through with a process pause longer than the ttl inserted between the last client-side check and the write. It is still correct.
- [ ] The design has been walked through with the client's clock running 1 s slow relative to the lock service's. It is still correct.
- [ ] The lock service's expiry is evaluated on the service's clock only; clients never compute expiry from their own wall clock without a margin.

## Operations

- [ ] Metrics exist for: acquisitions, denials, expiries without release, renew failures, and fenced-off writes. A non-zero fenced-off rate is expected and is evidence the fence is working; a non-zero expiry-without-release rate is a signal to investigate pauses.
- [ ] A runbook explains how to inspect the current holder and token, and how to recover a resource whose highest-seen token is ahead of the lock service's counter (for example after restoring the lock service from backup).
