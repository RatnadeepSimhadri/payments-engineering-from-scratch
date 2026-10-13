---
name: runbook-cdc-consumer
description: On-call runbook for a change-data-capture consumer (Debezium, logical decoding, binlog, change streams) that materializes a view, index, or cache from a transactional database
phase: 7
lesson: 11
tags: [cdc, operations, runbook, debezium, replication]
---

# CDC Consumer Runbook

For the person on call for a search index, cache, or warehouse table fed by change data capture. Each section is a symptom, the questions to ask, and the fix. Terms in parentheses are the lesson's names for the production concept.

## 0. Know your pipeline before the page

| Fact | Where to find it | Value |
|---|---|---|
| Source log and offset type (LSN, GTID, resume token) | connector config | |
| Connector offset store (Kafka offsets topic, slot, file) | connector config | |
| Log retention window (`wal_keep_size`, slot, `binlog_expire_logs_seconds`, topic retention) | database config | |
| Snapshot mode and how to trigger a re-snapshot | connector config / signal table | |
| Sink idempotency mechanism (per-key LSN guard, upsert, seen-key set) | sink code | |
| Reconciliation job that compares view to source, and its schedule | job scheduler | |

## 1. The view disagrees with the database

Ask:

- Is the consumer lagging (section 2)? A lagging view is stale, not wrong; wait or scale before investigating.
- Did the consumer restart recently? At-least-once redelivery after a crash (`poll(..., crashBeforeCommit)`) is correct only if the sink is idempotent. A counter or sum that drifted upward after a restart is a non-idempotent sink (`NaiveTotalsSink`). Fix the sink; rebuild the view.
- Was a snapshot taken recently? Snapshot rows arrive as `op: "r"`. A sink that only handles `c`/`u`/`d` drops them (view missing rows); a sink that treats `r` as `c` double-counts (view over-counting).
- Did a row's indexed attribute change? A sink that upserts by new key without deleting by `before` key leaves a dangling entry (Lesson 10). Check the sink handles `before`.
- Are deletes arriving? Deletes need `before` images (`REPLICA IDENTITY FULL` in PostgreSQL, `binlog_row_image=FULL` in MySQL). Without them the sink sees a key and no data and may ignore the event.

Fix: run the reconciliation audit (Lesson 10's dangling/missing check) to size the damage; patch the sink; re-snapshot if the damage is wide.

## 2. Lag is growing

Ask:

- Is the sink slow or the connector slow? Compare events/s read from the log against events/s acknowledged by the sink.
- Did write volume spike? A batch job on the source multiplies CDC volume; lag should recover when it ends.
- Is one partition hot? A single large account or merchant key sends all its changes to one partition and one consumer.

Fix: increase batch size (`poll(maxEvents)`), parallelise by key-partition (per-key order is preserved; cross-key is not), or add sink capacity. Do not skip events to catch up: a skipped LSN is a permanently wrong view until the next snapshot.

Watch: in PostgreSQL, a lagging or stopped slot pins WAL on the primary. Alert on `pg_replication_slots.confirmed_flush_lsn` distance and on primary disk. An abandoned slot will fill the disk and stop writes on the payments database.

## 3. The consumer fails to start: position not available

Signatures: `LogTruncatedException` here; `ChangeStreamHistoryLost` (MongoDB); `Could not find first log file name in binary log index file` (MySQL); `requested WAL segment ... has already been removed` (PostgreSQL); `OffsetOutOfRange` (Kafka).

Meaning: the offset points below the retention window. The changes between the offset and the oldest retained record are gone.

Fix: re-snapshot, then stream from the snapshot position. Never set the offset to the oldest available position and continue; that silently drops the missing range. After recovery, raise the retention window or add an alert on consumer downtime approaching it.

## 4. Schema changed on the source

Ask: was a column added, renamed, dropped, or retyped? Connector events now carry a different `after` shape.

- Added column: sinks that ignore unknown fields keep working; sinks with strict schemas fail. Prefer expand-then-contract on the source (Lesson 14) so the consumer can be updated between the two steps.
- Dropped or renamed column: the sink reads `null` or throws. Stop the consumer before the contract step, deploy the sink change, resume.
- Retyped amount column (integer minor units to decimal, or the reverse): treat as a money-handling incident; verify the sink still parses to integer minor units and never through a float.

## 5. Duplicate events observed downstream

This is expected. At-least-once is the contract. Check that:

- [ ] Every sink is idempotent: per-key LSN guard, natural upsert of the full `after` image, or a seen-key set for counters.
- [ ] Idempotency state is durable with the sink data, not in process memory, or a restart forgets what was seen.
- [ ] Counters and sums are derived by key, never incremented per event.

## 6. Rebuilding a view from scratch

1. Pause the consumer; note its offset.
2. Clear the sink's data and idempotency state together (a view with stale LSN guards will skip the rebuild).
3. Trigger a snapshot (`snapshot(table)`): head LSN recorded first, rows emitted as `r`, head stored as offset.
4. Resume streaming. Expect some rows to arrive twice at the seam; the LSN guard handles them.
5. Run the reconciliation audit; the view must show zero missing and zero dangling before it is put back behind reads.

## 7. After the incident

- Which of the six sections was it? Add the alert that would have caught it earlier.
- Was the sink idempotent? If not, that is the root cause, whatever triggered it.
- Did the reconciliation job detect it, and how long after? If never, the job is not comparing what matters.
