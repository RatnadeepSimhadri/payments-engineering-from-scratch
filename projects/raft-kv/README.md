# Raft Key-Value Store

**Level 3, Advanced. About 14 hours. Java (Python and Go ports follow later).**

Build a Raft-replicated key-value store on the deterministic simulated network from the Raft lessons, then prove its client history linearizable. Five nodes, seeded randomness, explicit partitions and crashes: every run is a pure function of a seed, so the bug you find at seed 17, tick 1203 is the bug you can replay until it is gone.

## What you will build

A `RaftNode` that implements Figure 2 of Ongaro and Ousterhout's paper on top of a `Storage` you write first, a `KVStore` state machine that applies committed `PUT`/`GET` commands, and a `Checker` that verifies the recorded client history against a register model. The simulation substrate (`Clock`, `Rng`, `Network`, `Message`, `Config`) and the test harness (`Cluster`, which checks Election Safety, Leader Append-Only, Log Matching, and State Machine Safety after every delivery and records client operations) are provided.

| Stage | Title | Hours | You implement | The grader proves |
|---|---|---|---|---|
| 01 | Term, Vote, and the Log | 2 | `Storage`: `currentTerm`, `votedFor`, 1-indexed log with sentinel, `append`, `truncate`, `lastIndex`, `lastTerm`, `termAt`, `entriesFrom`, `upToDate` | every persistence rule and exception, plus the provided scaffolding's determinism, delivery schedule, drops, partitions, crash and restart |
| 02 | Leader Election | 4 | `RaftNode` election: randomized timeouts from the node's seeded RNG, `RequestVote` and its receiver rules, majority counting, heartbeats, step-down | the seed-7 election, every receiver rule, 40 lossy seeds with at most one leader per term, partition and heal, votedFor across a restart |
| 03 | Log Replication | 4 | `propose`, `AppendEntries` with the consistency check, `nextIndex`/`matchIndex`, back-off, follower truncation, the election no-op | every consistency-check branch, truncation and idempotent re-delivery, catch-up, the stale-leader overwrite, Log Matching across 12 lossy seeds with a leader crash |
| 04 | Commit, Apply, and Linearizable Reads | 4 | `advanceCommitIndex` with the current-term rule, `applyCommitted`, `KVStore.apply`, the Wing-Gong `Checker` | commit by majority, the Figure 8 scenario, follower commit capping, client PUT/GET through the leader, a failed write that is never applied, and linearizable histories across lossy seeds |

## Prerequisites

- [Raft I: Terms, Leader Election, and a Simulated Network](../../phases/06-replication-and-consensus/05-raft-leader-election/docs/en.md): the network design and the election you rebuild here against a real log.
- [Raft II: Log Replication and Commit](../../phases/06-replication-and-consensus/06-raft-log-replication/docs/en.md): the consistency check, `nextIndex`/`matchIndex`, and the commit rule.
- [Linearizability Checking with Recorded Histories](../../phases/06-replication-and-consensus/12-linearizability-checking/docs/en.md): the Wing-Gong search you port to string registers in stage 4.

You should be able to read Figure 2 of the paper and say what each bullet is for before you start. The stage docs quote it by rule number.

## Commands

Run everything from the repository root. Java 21, standard library only.

```bash
# create a workspace with the stage-1 starter (Sim.java, Cluster.java, and the stubs)
python3 scripts/project_test.py raft-kv --lang java --init my-raft-kv

# grade one stage (stages 1..N all run, so regressions show up)
python3 scripts/project_test.py raft-kv --lang java --stage 1 --path my-raft-kv
python3 scripts/project_test.py raft-kv --lang java --stage 2 --path my-raft-kv
python3 scripts/project_test.py raft-kv --lang java --stage 3 --path my-raft-kv
python3 scripts/project_test.py raft-kv --lang java --stage 4 --path my-raft-kv

# grade everything and write a completion report
python3 scripts/project_test.py raft-kv --lang java --all --path my-raft-kv --report raft-kv-report.json

# run your own demo
cd my-raft-kv && javac -d .out *.java && java -cp .out Main
```

Each stage runs in a fresh temporary directory containing your workspace sources plus that stage's `StageNNTest.java`; your workspace is never modified. The whole suite runs in a few seconds; if a stage takes longer than ten, a node is almost certainly campaigning every tick.

## Workspace layout after `--init`

| File | Status | Contents |
|---|---|---|
| `Sim.java` | provided, do not edit | `Clock`, `Rng` (SplitMix64), `Role`, `MessageKind`, `LogEntry`, `Message`, `Config`, `Tracer`, `Node`, `Network` |
| `Cluster.java` | provided, do not edit | wires N nodes to one network; invariant checks after every delivery; faults; `propose`, `submit`, `history()` |
| `Storage.java` | stage 1 | persistent state |
| `RaftNode.java` | stages 2 to 4 | the server; fields and trivial accessors are given, every rule method is stubbed with `UnsupportedOperationException` |
| `KVStore.java` | stage 4 | the state machine; `apply` is stubbed |
| `Linearizability.java` | stage 4 | `Op`, `History`, `CheckResult` provided; `Checker` stubbed |
| `Main.java` | yours | the starter only exercises the scaffolding; replace it as the stages land |

Later stages ship a `starter/STAGE.md` listing the new signatures; nothing is copied after stage 1. You keep editing the same files.

## What you keep

- A complete, readable Raft core (election, replication, commit, apply) in about 250 lines, with every rule traceable to a Figure 2 bullet, that you can run against any seed and fault schedule.
- A deterministic simulation harness pattern (seeded RNG per actor, tick-driven network, invariants checked at every delivery) that transfers to any distributed protocol you need to test without flakiness.
- A linearizability checker over recorded histories and the habit of recording invoke/return times for client operations, which is how you find stale reads in a real service.
- A worked understanding of the two rules people get wrong in production Raft implementations: the up-to-date vote check and the current-term commit condition.

## Run with your own inputs

`Main.java` in your workspace is yours. Some things worth trying once the grader is green:

- **Hunt for a split vote.** Loop seeds on a 4-node cluster with `new Cluster(4, seed, 0.0, 3)` and print the first seed whose stable leader has `currentTerm() > 1`; then narrow the timeout range in `Config` and watch how many terms it takes.
- **Break the no-op.** Comment out the `NOOP` append in `becomeLeader`, run the stage-4 grader, and read which scenario catches it and at what tick.
- **Make reads cheap, then prove it unsafe.** Answer `GET` from the leader's `KVStore` without a log slot, record the op with `returnedAt = now()`, and feed the history to `Checker` during a partition: the stale leader will serve a read the checker rejects.
- **Replicate a ledger.** Replace `PUT balance:alice 75` with the balanced journal entries from the Ledger Engine project and have `KVStore.apply` refuse an entry that overdraws; the refusal must be deterministic so every node refuses the same one.
- **Count fsyncs.** `Storage.writes` is the number of stable writes. Print it per node after a lossy run and work out which rule costs the most; then read what etcd batches.

## Sources

- Ongaro, D. and Ousterhout, J., "In Search of an Understandable Consensus Algorithm (Extended Version)", USENIX ATC 2014. Figure 2 is the specification this project implements; Figure 8 is the stage-4 trap.
- Ongaro, D., "Consensus: Bridging Theory and Practice", PhD thesis, Stanford, 2014. Chapters 3 and 6 for the no-op, ReadIndex, and leases.
- Herlihy, M. and Wing, J., "Linearizability: A Correctness Condition for Concurrent Objects", ACM TOPLAS 12(3), 1990.
- Wing, J. and Gong, C., "Testing and Verifying Concurrent Objects", JPDC 17, 1993; Lowe, G., "Testing for Linearizability", CCPE 29(4), 2017. The search and the memo in `Checker`.
- Steele, G., Lea, D., and Flood, C., "Fast Splittable Pseudorandom Number Generators", OOPSLA 2014. The SplitMix64 generator in `Rng`.
