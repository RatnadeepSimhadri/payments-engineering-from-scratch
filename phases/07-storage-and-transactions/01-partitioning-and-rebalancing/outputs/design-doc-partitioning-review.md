---
name: design-doc-partitioning-review
description: Design-review template for any proposal to shard a table, topic, or service; forces the partition key, skew numbers, rebalancing unit, and cutover plan to be written down first
phase: 7
lesson: 1
tags: [partitioning, sharding, design-review, rebalancing, migration]
---

# Partitioning Design Review

Fill every section before the review. A section that says "TBD" is a reason to postpone the review, not a reason to skip the section.

## 1. What is being partitioned

- Dataset or stream: 
- Current size and growth per month (rows, bytes): 
- Peak write rate and peak read rate (per second): 
- Why vertical scaling is exhausted (name the limit that was hit): 

## 2. The partition key

- Key field(s): 
- Scheme: range / hash / hash-then-range (compound): 
- Hash function and why it is stable across languages and versions: 
- Fixed partition count P (if hash): ______ . Justify: maximum nodes expected in 3 years, per-partition overhead.
- Access patterns this key serves with a single-partition request: 
- Access patterns that become scatter-gather, and their expected frequency: 

## 3. Skew analysis (numbers required)

- Top 10 keys by request share over the last 30 days (from logs or metrics, not estimates): 
- Expected skew after partitioning (max partition load / mean): 
- Keys that exceed 5% of traffic alone, and the plan for each (salt with fanout N / dedicated partition / redesign): 
- Sequential or time-ordered component in the key? If yes, how is it prefixed to avoid a hot tail: 

## 4. Routing

- Where the partition-to-node table lives (coordination service, gossip, config file): 
- How a client learns the table changed (version/epoch field, server-side rejection, push): 
- What a node does with a request for a partition it does not own (reject with current version / proxy / serve stale): 

## 5. Rebalancing

- Unit of movement (whole partition / range split at key / ring arc): 
- Expected fraction of data moved when adding one node at target cluster size: 
- Who computes the plan, and is the plan deterministic given the same table: 
- Throttle on migration bandwidth so the copy does not starve production traffic: 

## 6. Migration sequence for one partition

| Phase | Writes go to | Reads go to | Exit condition |
|-------|--------------|-------------|----------------|
| Copy | source | source | snapshot fully loaded on destination |
| Catch-up / dual write | source + destination | source | destination has every write since snapshot |
| Cutover | destination | destination | routing table version bumped and acknowledged |
| Drop | destination | destination | grace period > max request timeout elapsed |

- How is the gap between "snapshot taken" and "dual write enabled" closed (log position replay / atomic switch): 
- Rollback at each phase: 
- Verification before drop (row counts, checksums per partition): 

## 7. Cross-partition operations

- List every operation that touches two or more partitions: 
- For each: is it atomic today? What provides atomicity after sharding (2PC, saga, redesign so it is single-partition): 
- Secondary indexes affected and whether they become local or global: 

## 8. Operations

- Dashboard: per-partition request rate, size, and skew ratio, with an alert on skew above ______ .
- Runbook link for: add node, remove node, move a partition, recover from a half-finished migration.
- Load test result at 2x current peak with the proposed layout: 

## Sign-off

- [ ] Partition key chosen from measured access patterns, not from the primary key by default
- [ ] P or ring parameters justified for three years of growth
- [ ] Skew computed from real traffic, hot keys named with a plan each
- [ ] Routing table versioned; nodes reject requests for partitions they do not own
- [ ] Migration sequence has a dual-write phase and a verified cutover; no "stop writes, copy, restart"
- [ ] Every cross-partition operation has an atomicity story or has been redesigned away
