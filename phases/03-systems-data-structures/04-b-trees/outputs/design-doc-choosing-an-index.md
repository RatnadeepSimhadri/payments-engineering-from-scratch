---
name: design-doc-choosing-an-index
description: Template for the storage-and-indexing section of a design document, with the B-tree vs LSM vs hash decision and the primary-key shape decision
phase: 3
lesson: 4
tags: [b-tree, index, storage, design-doc, primary-key]
---

# Storage and Indexing: Design Document Section Template

Copy this section into a design document for any new table or key-value store. Fill every field; "TBD" is a review finding.

## 1. Access patterns (answer before choosing anything)

| Question | Answer | Evidence |
|----------|--------|----------|
| Point lookups by primary key per second (p50 / peak) | | |
| Range or prefix scans? On which columns, how wide a window, how often? | | |
| Writes per second (inserts / updates / deletes), and their key distribution (ascending, random, clustered by tenant) | | |
| Read/write ratio | | |
| Working set size vs available RAM | | |
| Retention and deletion pattern (TTL sweep, soft delete, never) | | |

## 2. Structure decision

| If the dominant pattern is | Choose | Because |
|----------------------------|--------|---------|
| Point lookups only, no order needed | Hash index or hash map | O(1), no ordering cost |
| Point lookups plus range scans, moderate write rate | B+ tree (default in PostgreSQL, InnoDB, SQLite) | One page read per level, linked leaves for scans, in-place updates |
| Very high write rate, random keys, reads tolerate merging several files | LSM tree (RocksDB, Cassandra, LevelDB) | Sequential writes, compaction amortises random I/O; see Phase 3, Lesson 05 |
| Everything expiring before now, high churn | Timing wheel or per-window buckets | Avoids per-item tree maintenance; see Phase 3, Lesson 12 |

Decision: ______. Alternatives rejected and why: ______.

## 3. Primary-key shape

Numbers from Phase 3, Lesson 04, 200,000 keys into an order-64 tree with naive median splits:

| Key shape | Pages | Occupancy | Pages dirtied per insert | Notes |
|-----------|-------|-----------|--------------------------|-------|
| Sequential `BIGINT` | 6,249 | 51% (90% with rightmost-split fill factor) | the same rightmost page, repeatedly | Best write locality; reveals row count; hot spot on one page under heavy concurrency |
| Random (UUIDv4) | 4,630 | 69% | a random page, usually a cache miss | Worst write locality; in InnoDB scatters the table itself (clustered index) |
| Time-prefixed (ULID, UUIDv7, Snowflake) | like sequential | like sequential | mostly rightmost | Sequential locality without a global counter; see Phase 3, Lesson 13 |

Decision: ______. If random keys are required (external IDs, privacy), the index on them is a secondary index and the clustered or primary key is time-ordered.

## 4. Secondary indexes

For each: columns, purpose (which query), expected selectivity, and write cost (every index is one more B-tree every insert must update).

| Index | Columns | Query it serves | Selectivity | Covering? |
|-------|---------|-----------------|-------------|-----------|
| | | | | |

Rule: no index without a query that needs it and a measurement showing the planner uses it.

## 5. Page and fan-out estimate

- Page size: ______ (PostgreSQL 8 KB, InnoDB 16 KB, SQLite 4 KB default).
- Key + pointer size: ______ bytes, so approximately ______ entries per internal page.
- Expected rows: ______, so height ______ levels (`1 + log_fanout(rows / leaf_entries)`).
- Levels that fit in RAM permanently: ______ (all but the leaves, usually). So a cold point lookup costs ______ disk reads.

## 6. Operational checks for the runbook

- [ ] Fill factor set for ascending-key tables if the engine does not default to a rightmost-split optimisation.
- [ ] Bloat monitoring: index size versus `rows * entry_size / fill`; reindex threshold defined.
- [ ] `EXPLAIN (ANALYZE, BUFFERS)` (or equivalent) captured for each critical query at expected data volume, with buffer reads per query recorded as the baseline.
- [ ] Index-only scans possible for the hottest read (all needed columns in the index)?
- [ ] Deletion strategy does not leave half-empty pages indefinitely (TTL partitions by time rather than row deletes, if churn is high).
- [ ] Concurrency hot spot on the rightmost page assessed for sequential keys at peak insert rate.

## 7. What this design does not need

List the questions the store will not answer, so nobody adds an index for them later without a review: ______.
