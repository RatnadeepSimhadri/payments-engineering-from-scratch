---
name: design-doc-replication-choice
description: Design-review template for choosing between chain replication, synchronous primary-backup, and leader-based consensus for a replicated service, with the questions, the comparison table, and the invariants a reviewer should see tested
phase: 6
lesson: 10
tags: [replication, chain-replication, primary-backup, raft, design-review]
---

# Design Review: Choosing a Replication Scheme

Fill in every section. A review with an empty "Failure" section is not ready.

## 1. Workload

| Question | Answer |
|----------|--------|
| Reads per second (p50 / peak) | |
| Writes per second (p50 / peak) | |
| Read:write ratio | |
| Write latency budget (p99) | |
| Read latency budget (p99) | |
| Must a reader see their own just-acknowledged write? | yes / no (if no, say why support will not get tickets) |
| Object size and count | |

## 2. Membership and failure detection

| Question | Answer |
|----------|--------|
| Who decides a replica is dead? | (SWIM/gossip, health checks, a Raft group, a human) |
| Expected detection delay | |
| Who publishes the new configuration, and how is that service itself replicated? | |
| Can a demoted replica keep answering? What stops it? (fencing token, lease, epoch in every reply) | |

## 3. Candidate schemes

Fill in with your numbers; the defaults are from the lesson's simulation.

| Property | Chain replication | Synchronous primary-backup | Leader-based consensus (Raft) |
|----------|-------------------|----------------------------|-------------------------------|
| Write path | k hops serial | 1 parallel fan-out + 1 back | 1 parallel fan-out + majority back |
| Write latency vs k | grows with k | flat | flat |
| Read served by | tail only | primary only | leader only (followers need read-index or lease) |
| Busiest node's share of client work | updates on head, reads on tail | everything | everything |
| Replica divergence on failover | none (prefix property) | last in-flight write; re-sync needed | uncommitted suffix; truncated by new leader |
| Membership authority | external master | external master | built in (joint consensus / single-server) |
| Tolerates f failures with | f+1 replicas | f+1 replicas | 2f+1 replicas |
| Needs reliable FIFO links | yes | yes | no (retries built in) |

## 4. Decision

State the scheme and the two numbers from section 1 that drove it. If the answer is chain replication, state what the master is. If it is primary-backup, state the re-sync procedure on promotion. If it is consensus, state how reads are made linearizable.

## 5. Failure walkthrough

For each of the following, write what the client observes and how long the stall is:

- [ ] Head / primary / leader crashes with writes in flight
- [ ] Tail / backup / follower crashes with acks outstanding
- [ ] Middle replica crashes (chain) or one of several backups crashes
- [ ] The membership authority is unavailable for 30 seconds
- [ ] A replica is demoted but keeps running (stale tail, stale primary, stale leader)
- [ ] A new replica joins while traffic continues

## 6. Invariants the tests must check

- [ ] A read never returns a value that no acknowledged write produced (no uncommitted reads).
- [ ] A read issued after a write's acknowledgement reflects that write (read-your-writes).
- [ ] After any sequence of f failures, every acknowledged write is present on every surviving replica (no committed write is lost).
- [ ] Chain: Hist_successor is a prefix of Hist_predecessor at every delivery (Update Propagation).
- [ ] Chain: Hist_pred minus Hist_succ is inside Sent_pred at every delivery (Inprocess Requests).
- [ ] Primary-backup: after promotion, all backups' histories equal the new primary's.
- [ ] Amounts are integer minor units with a currency in every message and every replica (Phase 10, Lesson 01).

## 7. Operational notes

- Metrics: per-replica message counts by kind (updates, queries, acks), reconfigurations, client retries, stale-view redirects.
- Runbook link for: failure detection and repair, extension, master unavailability.
- Capacity: at what read rate does the tail (or primary) saturate, and what is the plan (more chains per shard, CRAQ-style reads, read replicas with explicit staleness)?
