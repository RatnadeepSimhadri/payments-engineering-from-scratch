---
name: checklist-read-path-consistency
description: Review checklist for any read endpoint served from replicas, covering which session guarantee it needs, how the token travels, and what to alert on
phase: 6
lesson: 2
tags: [replication, consistency, read-your-writes, session-guarantees, review]
---

# Read-Path Consistency Checklist

Use when a service adds its first read replica, moves an endpoint from the primary to replicas, or when support reports "the thing I just did is not there" or "it was there and then it disappeared". One unchecked box in the first two sections is a reason to keep the endpoint on the primary until it is fixed.

## 1. Which guarantee does this endpoint need?

Answer for the endpoint, not for the service.

- [ ] Does the caller's previous request in the same session write what this endpoint reads? If yes: **read-your-writes** is required.
- [ ] Will the caller load this view repeatedly and notice if it goes backwards (status page, balance, list of transactions)? If yes: **monotonic reads** is required.
- [ ] Does the response combine two or more keys where one is written only after another (authorization before capture, order before shipment)? If yes: **consistent prefix** is required, and both keys must be read from the same replica.
- [ ] Is the response the input to a decision that moves money? If yes: read from the leader or from a consensus-committed index. Freshness is not the concern; a correct-but-stale read is what a double spend exploits.
- [ ] None of the above (search, analytics, risk features, recommendations): any replica is correct and this checklist ends here.

## 2. The token

- [ ] Every write path returns the position of the write (LSN, GTID, offset, cluster time) to the caller, or stores it against the session server-side.
- [ ] Every read path accepts the token and uses it as a floor: a replica serves the read only if its applied watermark is at or past the floor.
- [ ] The floor for a session is `max(lastWriteLsn, lastReadLsn)`, and `lastReadLsn` is advanced to the position the read was served at, never lowered.
- [ ] The watermark a replica reports is the contiguous prefix it has applied, not the highest record it has seen.
- [ ] The token is signed or clamped so a client cannot present a floor above the leader's current position and force every read to the leader.
- [ ] Multi-shard sessions carry one token per shard or a single cluster-wide ordering (hybrid logical clock); a token from one shard is never compared with another shard's watermark.
- [ ] Monotonic-read endpoints pin a session to one replica (hash of session id) so the floor only has to intervene on failover.

## 3. The fallback

- [ ] When no replica satisfies the floor, the router reads from the leader, and the leader-read fraction is a metric on a dashboard with a budget attached.
- [ ] When the floor exceeds the leader's own position (after a failover that lost records), the read fails explicitly rather than serving a value below the floor.
- [ ] Replicas apply records in log order through a reorder buffer; a replica that applies on arrival is not eligible to serve any endpoint that needs consistent prefix.
- [ ] Reporting, backup, and maintenance workloads run on replicas that are excluded from the pool serving session-guaranteed reads, or lag-based eviction removes a replica from the pool when its lag crosses a threshold.

## 4. What to alert on

| Signal | How to measure | Means |
|--------|----------------|-------|
| Own-write misses | reads whose token floor was not met by the serving node (should be zero) | the router is not honouring the floor |
| Time-travel reads | a session served at a lower position than its previous read (should be zero) | missing stickiness or missing read floor |
| Leader-read fraction | floored reads that fell through to the leader / all floored reads | lag is high relative to the time between write and read |
| Lag in records | leader position minus replica watermark, per replica | queued work on the replica |
| Lag in time | now minus the leader commit time of the last applied record, per replica | how old the data a customer sees is; a stuck apply loop shows here with small record lag |
| Reorder-buffer size | records arrived but not yet applicable, per replica | a lost or very late record is blocking the prefix |

## 5. Before you ship

- [ ] A test reproduces the pays-then-sees-nothing bug against a replica with injected lag and shows the token fixes it.
- [ ] A test issues a sequence of reads for one session and asserts the served position never decreases.
- [ ] A test writes two dependent keys and asserts the second is never visible without the first on any replica.
- [ ] The runbook names who owns the leader-read budget and what to do when the fraction exceeds it: add replicas, fix the slow replica, or accept the leader load for that endpoint.
