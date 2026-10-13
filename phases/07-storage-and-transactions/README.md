# Phase 7: Partitioning, Storage & Transactions

> Durability is a promise; isolation is a contract.

14 lessons, 4 complete, ~22 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [Write-Ahead Logs and Crash Recovery](02-write-ahead-logs/)

Run the first complete lesson from the repository root:

```bash
python3 scripts/run_lesson.py phases/07-storage-and-transactions/02-write-ahead-logs --lang java
```

Keep the command, the exit code, and the test count as your first piece of evidence.

With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [Partitioning Strategies and Rebalancing](01-partitioning-and-rebalancing/) | Build | ~75 min | ⬚ | Range vs hash, hot keys, and moving a shard without downtime. |
| 02 | [Write-Ahead Logs and Crash Recovery](02-write-ahead-logs/) | Build | ~120 min | ✅ | fsync, torn writes, and a log you can replay after kill -9. |
| 03 | [Storage Engines: B-Tree vs LSM in Practice](03-storage-engines/) | Learn | ~75 min | ⬚ | Read amplification, write amplification, space amplification. |
| 04 | [Isolation Levels and Their Anomalies](04-isolation-levels-and-anomalies/) | Build | ~120 min | ⬚ | Dirty reads, lost updates, write skew: produce each one on purpose. |
| 05 | [Two-Phase Locking and Deadlock Detection](05-two-phase-locking/) | Build | ~90 min | ⬚ | Serializable the old way. |
| 06 | [MVCC and Snapshot Isolation](06-mvcc-and-snapshot-isolation/) | Build | ~120 min | ⬚ | Readers never block writers, and the write-skew they let through. |
| 07 | [Serializable Snapshot Isolation](07-serializable-snapshot-isolation/) | Build | ~90 min | ⬚ | Detect the dangerous structure instead of locking. |
| 08 | [Two-Phase Commit and Its Blocking Problem](08-two-phase-commit/) | Build | ~120 min | ✅ | Atomic across two ledgers, until the coordinator dies. |
| 09 | [Percolator and Spanner-Style Transactions](09-percolator-and-spanner-style-transactions/) | Learn | ~90 min | ⬚ | Timestamps as the coordinator. |
| 10 | [Secondary Indexes: Local vs Global](10-secondary-indexes/) | Build | ~75 min | ⬚ | Find all payments for a customer across shards. |
| 11 | [Change Data Capture](11-change-data-capture/) | Build | ~75 min | ⬚ | Turn the database log into an event stream. |
| 12 | [The Saga Pattern: Compensations Instead of Locks](12-saga-pattern/) | Build | ~120 min | ✅ | Reserve funds, capture, ship, and undo in the right order when step three fails. |
| 13 | [The Transactional Outbox and Dual-Write Problem](13-transactional-outbox/) | Build | ~90 min | ✅ | Commit the row and the event in one transaction or lose one of them. |
| 14 | [Schema Migrations at Scale: Expand, Migrate, Contract](14-schema-migrations-at-scale/) | Learn | ~60 min | ⬚ | Change a ledger table without stopping payments. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
