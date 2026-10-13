---
name: design-doc-quorum-configuration
description: Design-review template for choosing N, R, W, strict versus sloppy quorums, and the repair schedule for each data class in a leaderless replicated store
phase: 6
lesson: 3
tags: [quorums, replication, consistency, cassandra, design-review]
---

# Quorum Configuration Design Doc

Fill in one copy per table or key family, not per cluster. A reviewer should be able to reject the doc from section 2 alone if the data class is wrong for a quorum store.

## 1. Key family

| Field | Value |
|-------|-------|
| Key pattern | e.g. `merchant:<id>:settlement-currency` |
| Owner team | |
| Write rate (p50 / peak per second) | |
| Read rate (p50 / peak per second) | |
| Does any read feed a money movement? | yes / no; name the job |

## 2. Data class (pick one; this decides most of the rest)

- [ ] **Latest value is the whole truth** (config, feature flag, last-seen time). LWW is acceptable.
- [ ] **A sum or a balance** (wallet balance, daily limit, volume counter). Stop: use a ledger (Phase 10), a consensus log, or a CRDT counter. Do not store this as an LWW register.
- [ ] **Must be unique or exactly-once** (idempotency key, payout id). Stop: needs consensus or a conditional write on a leader.
- [ ] **Loss-tolerant** (cache, session hint). Any configuration works; choose for latency.

## 3. Replication parameters

| Parameter | Value | Justification |
|-----------|-------|---------------|
| N (replicas per key) | | failures to survive per operation, plus one during node replacement |
| Failure domains | rack / zone / region | three replicas in one zone is N=1 for a zone outage |
| W | | |
| R | | |
| `R + W > N`? | yes / no | if no, state the staleness you accept and why |
| Strict or sloppy | | sloppy breaks the intersection guarantee until handoff completes |
| Primary counts (`pr` / `pw`) if sloppy | | home-replica acks required even when sloppy |
| Local vs global quorum | `LOCAL_QUORUM` / `QUORUM` / `EACH_QUORUM` | cross-region round trip cost vs cross-region staleness |

## 4. The trade, stated in numbers

| Question | Answer |
|----------|--------|
| Replicas that can be down while writes still succeed | `N - W` |
| Replicas that can be down while reads still succeed | `N - R` |
| Expected write latency | the W-th fastest replica's latency |
| Expected read latency | the R-th fastest replica's latency |
| Staleness window when a replica misses a write | until read repair, handoff, or anti-entropy reaches it |
| What a stale read costs this business, per event | e.g. "one retry" or "48,000 EUR settled in the wrong currency" |

## 5. Conflict resolution

- Version format: `(timestamp, writerId)` LWW / version vector / CRDT.
- If LWW: what happens when two writers race? Name the update that is silently lost and confirm that is acceptable.
- If version vectors: who merges siblings, and what is the merge rule?
- Clock source for timestamps, and what an NTP step back does to LWW winners.

## 6. Repair plan

| Mechanism | Enabled | Setting | Gap it leaves |
|-----------|---------|---------|---------------|
| Hinted handoff | | max hint window | hints lost if the holder dies |
| Read repair | | on every digest mismatch / probabilistic | keys nobody reads |
| Anti-entropy (`nodetool repair` or equivalent) | | schedule, Merkle depth | none, but must run within `gc_grace_seconds` or deletes resurrect |

## 7. Reviewer checklist

- [ ] The data class in section 2 is not a sum, a balance, or an exactly-once key.
- [ ] Any read that feeds a money movement uses `R + W > N` on a strict (or `pr`-bounded) quorum.
- [ ] Replicas are spread across the failure domain the SLO is written against.
- [ ] Sloppy quorums are justified by a named availability requirement, not by default.
- [ ] The doc names the update LWW will drop in a race, and the owner accepts it.
- [ ] Anti-entropy runs more often than the tombstone grace period.
- [ ] Alerting exists on hint backlog size and repair lag, not only on node health.
- [ ] A test or chaos run reproduces "write acked, read stale" for this configuration and the result is recorded here.
