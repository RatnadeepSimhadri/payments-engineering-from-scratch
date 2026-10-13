---
name: decision-card-secondary-indexes
description: One-page design-review card for adding a secondary index to a partitioned table, local vs global, with the cost formulas and the consistency checks that must be in the design
phase: 7
lesson: 10
tags: [indexing, partitioning, consistency, design-review]
---

# Secondary Index Decision Card

Use when a design proposes querying a partitioned table by anything other than its partition key. Fill in every blank before approving.

## 1. The access pattern

- Query: `find <rows> where <attribute> = <value>` ordered by `________`
- Shard count `N` today: `____`; in 2 years: `____`
- Typical number of distinct shards holding hits for one value, `k`: `____` (measure it; do not guess)
- Queries per second: `____`; p99 latency budget: `____ ms`
- Who runs it: customer-facing path / support tool / batch job (circle one)

## 2. Read cost

| Strategy | Requests per query | Latency shape |
|---|---|---|
| Scatter-gather over local indexes | `N` | slowest of N shards |
| Global index, then fetch | `1 + k` | one index hop, then slowest of k shards |
| Global covering index | `1` | one index hop |

- [ ] `N` and `k` filled in above; the global index is justified only if `1 + k` is well below `N`
- [ ] Fan-out load has been multiplied through: `queries/s x N` extra requests on shards that also serve the primary path
- [ ] If the attribute has a hot value (one merchant, one platform account), the design names which index partition absorbs it and what its limit is

## 3. Write cost

- Logical writes/s to the table: `____`
- Global indexes proposed: `g = ____`; physical writes per logical write: `1 + g`
- [ ] SYNC maintenance: every write now includes a remote write; the design states what happens when the index write fails after the row commits (2PC, compensating delete, or accept inconsistency)
- [ ] ASYNC maintenance: the design names the queue (change stream, outbox, service bus), its durability, its ordering guarantee per key, and its expected lag at peak

## 4. Consistency the readers will observe

- [ ] Staleness window measured or bounded: `____ ms` at peak write rate
- [ ] Read-your-own-writes: the screen that follows a write either reads by primary key, reads from the shard's local index, or merges the written row client-side
- [ ] Dangling entries: every global-index query fetches the row from its shard and filters missing rows, OR the design explains why ghosts are acceptable
- [ ] Attribute changes: a change to the indexed attribute emits a delete under the old value and an insert under the new one, in that order, on the same queue
- [ ] Idempotent apply: inserting an existing locator or deleting an absent one is a no-op, so the consumer can replay after a crash

## 5. Bootstrap and repair

- [ ] Backfill plan: snapshot position recorded, full scan of every shard, replay of the stream from the snapshot position
- [ ] Audit job: compares index to table (dangling) and table to index (missing) on a schedule; alert thresholds set
- [ ] Rebuild runbook: how to drop and recreate the index without taking the primary path down

## 6. The decision

| Choose | When |
|---|---|
| No index, scatter-gather | `N` is small (under ~8), the query is rare, or results must be exactly current |
| Local index | The query always includes the partition key (DynamoDB LSI shape), or fan-out is acceptable and freshness is not negotiable |
| Global index, fetch rows | `k` is much less than `N`, readers tolerate a bounded lag, and ghosts must never be shown |
| Global covering index | As above, and the projected attributes are immutable or their staleness is acceptable for this screen |

Decision: `________________` Reviewer: `________` Date: `________`
