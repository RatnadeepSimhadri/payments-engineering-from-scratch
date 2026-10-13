# Phase 6: Replication & Consensus

> Agreement is expensive. Know exactly what you are paying for.

12 lessons, 9 complete, ~19 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [Replication: Leader-Based, Multi-Leader, Leaderless](01-replication-strategies/)


With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [Replication: Leader-Based, Multi-Leader, Leaderless](01-replication-strategies/) | Learn | ~75 min | ✅ | Who accepts writes decides how conflicts happen. |
| 02 | [Replication Lag and Read-Your-Writes](02-replication-lag-and-read-your-writes/) | Build | ~75 min | ✅ | A customer pays, refreshes, and sees no payment. |
| 03 | [Quorums: R + W > N and Its Limits](03-quorums/) | Build | ~75 min | ✅ | Sloppy quorums, hinted handoff, and read repair. |
| 04 | [Single-Decree Paxos from Scratch](04-paxos/) | Build | ~120 min | ✅ | Proposers, acceptors, and the two promises that make it safe. |
| 05 | [Raft I: Terms, Leader Election, and a Simulated Network](05-raft-leader-election/) | Build | ~150 min | ✅ | Build the election with a deterministic network so you can replay every bug. |
| 06 | [Raft II: Log Replication and Commit](06-raft-log-replication/) | Build | ~150 min | ✅ | AppendEntries, matchIndex, and the Log Matching property. |
| 07 | [Raft III: Safety Proofs, Membership Changes, Snapshots](07-raft-safety-and-membership/) | Build | ~120 min | 🚧 | Joint consensus and why you cannot just swap the config. |
| 08 | [CRDTs: Counters, Sets, and Convergence](08-crdts/) | Build | ~90 min | ✅ | A PN-counter for balances that can be reconciled without coordination. |
| 09 | [Conflict Resolution: LWW, Version Vectors, Merge Functions](09-conflict-resolution/) | Build | ~75 min | ✅ | Last-writer-wins loses data by design. |
| 10 | [Chain Replication and Primary-Backup](10-chain-and-primary-backup/) | Build | ~75 min | 🚧 | Strong consistency with high read throughput. |
| 11 | [Byzantine Fault Tolerance: PBFT in Outline](11-byzantine-fault-tolerance/) | Learn | ~75 min | 🚧 | 3f + 1 and why most payment systems do not need it. |
| 12 | [Linearizability Checking with Recorded Histories](12-linearizability-checking/) | Build | ~90 min | ✅ | Prove your replicated register wrong with a history checker. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
