---
name: checklist-consistency-claim-review
description: Review checklist for any design document, API, or data store that claims a consistency level, with the history-checking procedure that tests the claim
phase: 6
lesson: 12
tags: [linearizability, consistency, replication, testing, review, jepsen]
---

# Consistency Claim Review Checklist

Use on any design document, API contract, or vendor evaluation that uses the words "strongly consistent", "linearizable", "read-your-writes", or "consistent reads". Each item either pins down what the claim means or tests it. One unchecked box is a reason to request changes or to run the test before relying on the claim.

## 1. What exactly is claimed

- [ ] The claim names the object (a register per key? a key-value map? a ledger with transactions?) and the operations (read, write, CAS, transaction) it covers. "The database is consistent" is not a claim; "a read of key k invoked after a write of k returned observes that write or a later one" is.
- [ ] The claim names the consistency model using its definition, not a marketing synonym: linearizable (real-time order across all clients), sequentially consistent (per-client program order only), read-your-writes (one client's own writes), eventual (none of the above).
- [ ] The claim states which read paths it applies to. Follower reads, cache reads, read replicas, and "stale OK" flags are listed explicitly as excluded or included.
- [ ] The claim states what happens to in-flight operations on failover or partition: are timed-out writes guaranteed to have either happened or not, and can a client find out which?

## 2. Where the claim can break

- [ ] Every read path that bypasses the write path's ordering mechanism is identified (serving from a replica's state machine, from a cache, from a deposed leader, from a snapshot).
- [ ] For a Raft or Paxos backed store: reads either go through the log, or use ReadIndex / a leader lease, or are documented as not linearizable. A leader that answers from its state machine without confirming leadership is a stale-read bug waiting for a partition.
- [ ] For a primary-backup store with asynchronous replication: reads from backups are documented as sequentially consistent at best, with a bound on staleness if one is enforced.
- [ ] Client retries on timeout are examined: a retried write that lands twice is a correctness problem for non-idempotent operations and will show up as a history the model rejects.

## 3. Recording a history to test the claim

- [ ] Clients record `invokedAt` immediately before sending and `returnedAt` immediately after receiving, from one clock (or from clocks whose maximum skew is known and used to widen every interval).
- [ ] Each recorded operation carries its client id, operation type, key, argument, and observed result. Timed-out operations are recorded as incomplete, not dropped and not assumed failed.
- [ ] The workload mixes reads and writes on a small key space (so that operations actually contend) with enough concurrency that intervals overlap, and runs while faults are injected: partitions, leader kills, clock jumps, slow disks.
- [ ] The history is checked with a linearizability checker against a sequential model of the claimed object (this lesson's `Checker` for registers; Porcupine or Knossos for richer models and incomplete operations), partitioned by key where keys are independent.
- [ ] A passing history is recorded with its seed or fault schedule so it can be rerun; a failing history is kept as a regression test and minimised by deleting operations until the verdict flips.

## 4. Reading the verdict

| Verdict | Meaning | Action |
|---------|---------|--------|
| Linearizable, all histories | No violation observed under these faults | Keep running; this is evidence, not proof. Increase concurrency and fault variety. |
| Not linearizable, sequentially consistent | A replica served a value older than an acknowledged write | Find the read path that bypassed ordering (section 2). Either fix it or relabel the claim. |
| Not sequentially consistent | A client saw its own operations out of order, or a value was lost or duplicated | A write-path bug: lost acknowledged write, duplicated retry, or split brain. Treat as a safety incident. |
| Unknown / budget exceeded | The history was too concurrent for the checker to decide | Shorten the history, lower concurrency, partition by key, or use a checker with a longer timeout. Do not record it as a pass. |

## 5. Before approving

- [ ] The documented consistency level matches the strongest verdict the recorded histories support, for each read path separately.
- [ ] Code that gates a money movement on a read (balance checks, limit checks, idempotency lookups) uses only read paths verified linearizable, or is explicitly designed to tolerate staleness (holds, reservations, compensating transactions).
- [ ] The history recorder and checker run in CI against the staging cluster under fault injection, and a failing history blocks the release.
