---
name: runbook-raft-log-divergence
description: Incident runbook for a replicated log whose replicas disagree, with the symptom-to-cause table for the three classic replication bugs
phase: 6
lesson: 6
tags: [raft, replication, log, incident, runbook, consensus]
---

# Replicated Log Divergence Runbook

Use when monitoring or a reconciliation job reports that replicas of a consensus-backed log (Raft, etcd, Kafka ISR/KRaft, a primary-backup WAL) hold different entries, different commit indexes, or different state-machine results. The goal is to decide within the first thirty minutes whether you are looking at normal uncommitted divergence or a safety violation, because the responses are opposite: the first is left alone and the second is a stop-the-world incident.

## 1. Capture before you touch anything

- [ ] Dump every replica's log metadata: `currentTerm`, `votedFor`, log length, the `(index, term)` of the last entry, `commitIndex`, `lastApplied`. Record the wall-clock time of each dump; they will not be simultaneous.
- [ ] Dump the full log (or the last 200 entries) from every replica, including entries beyond `commitIndex`.
- [ ] Record which node believes it is leader and for which term. Two nodes claiming leadership in different terms during a partition is normal; two in the same term is a safety violation (Lesson 05).
- [ ] Freeze client traffic to the affected group if the state machine holds money. Reads from a possibly-deposed leader are the common way divergence reaches customers.

## 2. Classify with the Log Matching property

For each pair of replicas A and B, find the highest index `k` where `A[k].term == B[k].term`.

| Observation | Classification | Action |
|-------------|----------------|--------|
| A and B are identical through `k`, and differ only above `k`, and every differing entry on either side is above that side's `commitIndex` | Normal uncommitted divergence (a partitioned or lagging replica holding a tail the leader will truncate) | None. Watch it resolve on the next successful AppendEntries. |
| A and B are identical through `k`, and an entry above `k` on one side is at or below that side's `commitIndex` | **Lost commit.** A committed entry is absent from a replica that should have it | Section 4. Safety violation. |
| A and B share `(k, term)` but differ at some index below `k` | **Log Matching violation.** The consistency check was bypassed | Section 4. Safety violation. |
| Two replicas have different entries at the same index, both at or below their `commitIndex` | **State Machine Safety violation.** Two different commands committed at one index | Section 4. The most severe case: state machines have diverged. |
| One replica's `commitIndex` exceeds its log length | Volatile state corruption or a rule-5 `min` bug | Restart that replica; its commit index is recomputable. Then section 4 if it recurs. |

## 3. Normal divergence: what you should see

- The replica with the extra tail is, or recently was, on the minority side of a partition or a stale leader.
- Its tail entries carry an older term than the current leader's entries at the same indexes.
- Its `commitIndex` is below the first index of the tail.
- Within one or two heartbeat intervals of connectivity returning, the leader's AppendEntries produces a consistency-check rejection, a `nextIndex` back-off, and then a truncate-and-append on the follower. Logs then match.

If the tail persists for many heartbeat intervals with connectivity restored, the leader's back-off is stuck (check for a `nextIndex` that never decreases, or a reply path that drops rejections). That is a liveness bug, not a safety bug: no data is wrong, but the follower will never catch up.

## 4. Safety violation: the three implementation bugs that cause each symptom

| Symptom | Most likely cause | How to confirm |
|---------|-------------------|----------------|
| Committed entry missing on a replica that acknowledged it | Acknowledging before the persistent write (reply sent, then crash, then entry lost). Figure 2: "updated on stable storage before responding to RPCs" | Correlate the replica's crash/restart time with the entry's commit time; check whether the storage layer fsyncs before the reply is enqueued |
| Different committed entries at one index, both with old terms | Commit rule counted replicas of an earlier-term entry (missing `log[N].term == currentTerm` check), the Figure 8 schedule | Find the leader that advanced `commitIndex` past that index and check the term of the entry versus the leader's term at the time |
| Committed entries truncated after a heartbeat or retransmission | Follower truncates from `prevLogIndex` on every AppendEntries instead of from the first conflicting entry (rules 3 and 4 merged incorrectly) | Reproduce with a reordered duplicate AppendEntries in a test; the lesson's test `heartbeat with an older prefix does not truncate newer matching entries` is the template |
| Two leaders in one term, each committing | Election bug: `votedFor` not persisted, or majority computed over reachable rather than configured nodes | Lesson 05's checklist |
| Follower applied past its log | Rule 5 missing the `min(leaderCommit, lastNewIndex)` | Check the follower's `commitIndex` against its log length at the time of the apply |

Response for any safety violation: stop the leader from accepting writes, preserve every replica's storage as evidence, and reconstruct the correct log by taking the longest prefix that is identical on a majority of replicas and whose last entry's term matches the highest term seen on a majority. Any entry outside that prefix that was acknowledged to a client is a lost write and must go to the business-level reconciliation process (Phase 10, Lesson 08). Do not "pick the longest log" and do not restart replicas with cleared storage until the prefix is recorded.

## 5. Prevention checklist for the post-incident review

- [ ] The follower's reply is enqueued only after the log append and term/vote write have reached durable storage.
- [ ] The commit rule checks `log[N].term == currentTerm`, and the leader appends a no-op on election so old-term entries commit promptly.
- [ ] The follower truncates only from the first entry that conflicts by term, and leaves matching entries alone.
- [ ] `commitIndex` on a follower is capped at the index of the last entry it actually holds.
- [ ] Reads that promise linearizability go through a leader-confirmation step (ReadIndex or a lease), not straight to the state machine.
- [ ] A Log Matching checker runs in the deterministic test suite after every delivery, as in this lesson's `Cluster.checkInvariants`, and every incident seed is added as a regression.
