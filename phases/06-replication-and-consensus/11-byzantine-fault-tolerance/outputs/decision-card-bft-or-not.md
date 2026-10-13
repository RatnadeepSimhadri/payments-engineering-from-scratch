---
name: decision-card-bft-or-not
description: One-page decision card for choosing between crash-fault tolerance with integrity checks, a trusted operator, reconciliation, and Byzantine fault tolerance for a payments component
phase: 6
lesson: 11
tags: [bft, pbft, raft, consensus, design-review, decision]
---

# Do We Need Byzantine Fault Tolerance?

Use in a design review when someone proposes BFT consensus, a "blockchain", or a permissioned ledger for a payments component, or when an incident involved a node that was wrong rather than down.

## 1. Who runs the replicas?

| Answer | Go to |
|--------|-------|
| One organisation, one deploy pipeline, one set of admins | section 2 |
| Several organisations, but one of them could act as operator under a rulebook | section 3 |
| Several organisations, none of which the others will accept as operator | section 4 |

## 2. One organisation: crash-fault tolerance plus integrity

BFT buys nothing here: a single compromised credential or bad build reaches f + 1 replicas at once, so the effective f is zero. The Byzantine faults you actually see (bit flips, torn writes, bugs that corrupt and keep running) are accidental and independent. Turn them into crashes:

- [ ] Every log entry and snapshot carries a checksum written with it and verified before acknowledge and before apply
- [ ] Followers verify the leader's entries before writing; leaders verify followers' acknowledgements against entry hashes
- [ ] Candidates' claimed log positions are checked against entry hashes by voters where possible
- [ ] The state machine keeps a running state hash agreed at each snapshot; a node that diverges halts and alerts
- [ ] Replica-to-replica traffic is mutually authenticated (mTLS); nobody outside the cluster can inject a message
- [ ] f is written down (nodes = 2f + 1) and the replicas are spread so no single rack, zone, or maintenance window takes f + 1

Use Raft or Paxos (etcd, ZooKeeper, Consul, a database's own replication). Spend the budget saved on testing and backups.

## 3. Several organisations with a possible operator

This is how card networks, RTGS systems, ACH, and SWIFT work. The operator orders and settles; members authenticate messages; disputes go to the rulebook and, ultimately, to law.

- [ ] Messages between members and operator are signed; replay and ordering are defined by the operator's protocol
- [ ] Each member keeps its own ledger and reconciles against the operator's statements (Phase 10, Lesson 08)
- [ ] The dispute process is written down, with timelines and liability
- [ ] The operator itself runs section 2 internally

Faster, cheaper, and with more recourse than BFT. Choose BFT over this only when the parties will not accept the operator, which is a governance decision, not an engineering one.

## 4. No operator: BFT, with the costs written down

- [ ] n >= 3f + 1 and f is explicit; replicas are hosted by *different* organisations so faults are independent
- [ ] Every message is authenticated (MACs or signatures); the view-change proof is verifiable by every replica
- [ ] Clients wait for f + 1 matching replies from distinct replicas; there is no single gateway whose answer is trusted alone
- [ ] Finality is deterministic (PBFT, HotStuff, Tendermint-style), or the confirmation depth of a probabilistic system is recorded as a risk parameter
- [ ] Measured commit latency with the real n across the real network fits the product budget; message volume per request is roughly 2n² for PBFT
- [ ] Membership changes (adding or removing a replica) have a protocol and a governance process
- [ ] There is still a reconciliation and dispute process, because BFT protects the shared log, not the correctness of what members submit to it

## 5. Numbers to keep in view

| | Crash-fault (Raft) | Byzantine (PBFT) |
|--|-------------------|------------------|
| Tolerate 1 fault | 3 nodes | 4 nodes |
| Tolerate 2 faults | 5 nodes | 7 nodes |
| Messages per decision | about 2n | about 2n² |
| Client needs | 1 reply | f + 1 matching replies |
| Leader change needs | majority vote | 2f + 1 signed certificates |

## 6. The one-line test

If you cannot name the f + 1 independent parties whose simultaneous misbehaviour the system would survive, you do not have a BFT system; you have an expensive crash-fault-tolerant one.
