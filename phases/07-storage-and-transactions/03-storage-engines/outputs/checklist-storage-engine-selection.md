---
name: checklist-storage-engine-selection
description: Decision checklist for choosing between a B-tree engine and an LSM engine for a table or keyspace, with the measurements to gather first and the production knobs to review
phase: 7
lesson: 3
tags: [storage-engine, b-tree, lsm, rocksdb, postgresql, amplification]
---

# Storage Engine Selection Checklist

Run this once per table or keyspace, not once per system. Mixed answers are normal: a ledger service commonly keeps balances on a B-tree and events on an LSM.

## 1. Measure before choosing

- [ ] Write rate at peak (rows/s and bytes/s) and its growth over 12 months
- [ ] Ratio of inserts : updates : deletes
- [ ] Key arrival order: sequential (timestamps, sequences) or random (UUIDs, hashes)
- [ ] Read mix: point lookups vs range scans, and which key leads each
- [ ] Working set size vs available memory
- [ ] Number of secondary indexes and whether their keys are random relative to arrival order
- [ ] Tail-latency requirement (p99.9) and whether a multi-second stall is tolerable

## 2. Decision procedure (answer in order; stop at the first match)

| # | Question | If yes |
|---|----------|--------|
| 1 | Updated in place with high locality (balances, account state)? | B-tree |
| 2 | Append-mostly with sequential keys, range-scanned by time? | Either; prefer LSM if storage cost per byte matters (compression), B-tree if scan latency matters |
| 3 | Append-mostly with random keys (idempotency keys, UUID records)? | LSM |
| 4 | Range scans dominate reads? | B-tree |
| 5 | Working set far larger than memory, point reads dominate? | LSM with Bloom filters |
| 6 | Predictable tail latency outweighs throughput? | B-tree |

## 3. If B-tree (PostgreSQL, InnoDB, WiredTiger)

- [ ] `fillfactor` below 100 on hot update tables so HOT updates skip index maintenance
- [ ] Autovacuum tuned per table (`autovacuum_vacuum_scale_factor`) for the hot ones; alert on `n_dead_tup / n_live_tup > 0.2`
- [ ] Alert on long-running transactions that hold back the vacuum horizon (`pg_stat_activity.xact_start` older than N minutes)
- [ ] Checkpoint spacing chosen deliberately: `checkpoint_timeout` and `max_wal_size` trade recovery time against write amplification
- [ ] Group commit enabled where fsync latency is the bottleneck (`commit_delay`, `innodb_flush_log_at_trx_commit = 1` kept, not relaxed)
- [ ] Secondary indexes justified by a query each; random-key indexes on high-insert tables flagged
- [ ] Measured: `pg_stat_wal.wal_bytes`, `pg_stat_bgwriter.buffers_checkpoint`, index and table bloat

## 4. If LSM (RocksDB, Pebble, Cassandra, ScyllaDB)

- [ ] Compaction style chosen: leveled for read-heavy and space-sensitive, tiered or universal for write-heavy with disk headroom
- [ ] Compaction threads and rate limiter sized for peak ingest; stall triggers (`level0_slowdown_writes_trigger`, pending compaction bytes) reviewed, not disabled
- [ ] Bloom filter bits per key set (10 bits gives ~1% false positives) and filter blocks pinned in cache
- [ ] Deletes designed to drop whole files (time-partitioned keys, `DeleteRange`, TTL compaction) rather than per-key tombstones
- [ ] Disk headroom: 50% for tiered compaction, 10-20% for leveled
- [ ] Memtable size and count chosen so flushes are large and sequential but recovery replay stays short
- [ ] Measured: `rocksdb.bytes.written`, `rocksdb.compact.write.bytes`, `rocksdb.flush.write.bytes`, stall counters, per-level file counts

## 5. Both

- [ ] WAL on a device with power-loss protection, or fsync latency measured and group commit relied on
- [ ] Write amplification measured from engine statistics after a week of production traffic and compared with the estimate used to choose
- [ ] Capacity model: sustained write rate is bounded by (disk write bandwidth / write amplification) for LSM, and by (random read IOPS / cold index pages touched per write) for B-tree
- [ ] Runbook for the engine's characteristic failure: write stall and tombstone build-up for LSM; bloat and hot-page contention for B-tree

## Sign-off

- [ ] The choice was made per table from measured access patterns
- [ ] The three amplifications were estimated with numbers and will be re-measured in production
- [ ] The characteristic failure mode has an alert and a runbook before go-live
