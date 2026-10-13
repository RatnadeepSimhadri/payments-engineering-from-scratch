---
name: runbook-lsm-store-operations
description: Operations runbook for a service backed by an LSM store (RocksDB, Cassandra, LevelDB, or in-house) — what to graph, what the alarms mean, and how to change compaction safely
phase: 3
lesson: 5
tags: [lsm, rocksdb, cassandra, compaction, runbook, operations]
---

# LSM Store Operations Runbook

For any service whose storage engine is a log-structured merge tree. Fill the engine-specific metric names in the brackets.

## 1. The four numbers to graph

| Metric | What it is | Healthy range | Source |
|--------|------------|---------------|--------|
| Write amplification | disk bytes written / application bytes written | 4-8 size-tiered, 10-30 leveled | [`rocksdb.compact.write.bytes` + `rocksdb.flush.write.bytes`] / [`rocksdb.bytes.written`] |
| Read amplification | tables or blocks consulted per point lookup | < 3 block reads per get on average | [`rocksdb.get.hit.l0/l1/l2andup`], Bloom useful ratio |
| Space amplification | live disk bytes / logical data size | 1.1-2.0 | engine SST total / estimated live size |
| Compaction backlog | pending compaction bytes or file count per level / tier | L0 files below trigger (default 4); pending bytes below soft limit | [`rocksdb.compaction-pending`, `estimate-pending-compaction-bytes`] |

Also graph memtable flush count per minute and Bloom filter useful ratio (`bloom negatives / bloom probes`), which should be above 95%.

## 2. Symptoms and causes

| Symptom | Likely cause | First action |
|---------|--------------|--------------|
| Write latency spikes every few seconds | Memtable flush stalls or L0 file count at the stop trigger | Check compaction backlog; raise background compaction threads; check disk write bandwidth |
| Read latency rising over days with constant traffic | Compaction falling behind so reads consult more tables | Confirm backlog growing; schedule manual compaction in a low-traffic window |
| Disk usage far above logical size | Shadowed versions and tombstones not yet compacted; or a compaction output coexisting with its inputs | Check space amplification; verify compaction is running; never delete SST files by hand |
| Deleted data reappears | Tombstone dropped before every replica or lower level had the delete | Check `gc_grace_seconds` versus repair interval (Cassandra); check for manual file deletion |
| Range scans slow, point reads fine | Many tombstones in the scanned range (TTL expiry, bulk deletes) | Compact the range; redesign deletes as partition drops or time-bucketed tables |
| Sudden read-amplification jump after a deploy | Bloom filters disabled or key format changed so the filters miss | Verify filter configuration and that keys are hashed as stored |

## 3. Why deletes do not free space

A delete writes a tombstone. Space returns only when a compaction merges the tombstone's table with the table holding the value and can prove no older copy survives (Phase 3, Lesson 05, Step 7). Plan for it:

- Capacity: budget roughly 2x the logical size for size-tiered compaction, 1.3x for leveled.
- Bulk deletes: prefer dropping a whole table, partition, or column family over issuing millions of tombstones.
- TTL data: use the engine's TTL (compaction filter) rather than application-level deletes.
- Wide tombstones over ranges: check the engine supports range tombstones (RocksDB `DeleteRange`) before relying on them.

## 4. Changing compaction strategy safely

- [ ] Measure the four numbers above for a week on the current strategy; keep the graphs.
- [ ] State which number you are trading for which (size-tiered to leveled: lower read and space amplification for higher write amplification).
- [ ] Confirm disk write bandwidth headroom covers the new write amplification at peak.
- [ ] Change on one replica or one shard first; compare the four numbers for 24 hours.
- [ ] Expect a one-time full rewrite of the data when switching strategies; schedule it.
- [ ] Keep the old configuration ready to roll back; the files are compatible, the policy is not.

## 5. Compaction backlog relief, in order

1. Confirm the disk is not the bottleneck (write bandwidth, I/O queue depth).
2. Raise background compaction threads (RocksDB `max_background_jobs`; Cassandra `concurrent_compactors`).
3. Raise the compaction rate limit if one is set.
4. Temporarily reduce write load (shed, queue, or rate-limit producers).
5. Manual compaction of the most backed-up level or tier, off-peak.
6. If the backlog never clears at steady state, the hardware is undersized for the write rate times the amplification: this is a capacity problem, not a tuning problem.

## 6. Never do this

- Delete SST files by hand. The manifest references them; the store will not open.
- Disable Bloom filters to "save memory" on a point-lookup workload.
- Run a major compaction on every node of a cluster simultaneously.
- Treat the memtable as durable. If the write-ahead log is off, acknowledged writes are lost on crash.
