---
name: checklist-crdt-fitness
description: Design-review checklist for any proposal to replicate state with a CRDT instead of a leader or quorum
phase: 6
lesson: 8
tags: [crdt, replication, design-review, invariants]
---

# CRDT Fitness Checklist

Use when a design proposes coordination-free multi-region writes (a CRDT type, Riak data types, Redis Enterprise active-active, a sync engine). Every unchecked box is a question for the author, not a veto.

## 1. Is the invariant local or global?

- [ ] Every invariant the data must satisfy is written down as a predicate.
- [ ] Each predicate is classified: **local** (decidable from one replica's state: "count is a sum of deltas", "set contains x") or **global** (needs all replicas: "balance >= 0", "username unique", "at most one active session").
- [ ] For every global invariant, the design states which remedy it uses: coordinate (leader, quorum, consensus) or re-partition the invariant (per-replica escrow / bounded counter). "The replica checks before writing" is neither.
- [ ] The business owner has confirmed what happens when the global invariant is violated during a partition (negative balance collected on next top-up? order cancelled? manual review?).

## 2. Which type, and what do concurrent updates mean?

| Data | Type | Concurrent-update semantics to confirm with product |
|------|------|-----------------------------------------------------|
| Sum of independent deltas (balance, inventory, likes) | PN-Counter | Both deltas apply; result may cross any threshold |
| Membership with add and remove (cart, tags, sessions) | OR-Set | Add-wins or remove-wins; pick one and document it |
| Single value overwritten (profile field, config) | LWW-Register | Losing write is discarded without trace |
| Multiple fields updated independently | Map of the above | Each field merges on its own; no cross-field atomicity |

- [ ] The chosen type is named and the row above is acknowledged in the design.
- [ ] For LWW: the author has listed which writes can be silently lost and confirmed that is acceptable.
- [ ] For OR-Set: add-wins vs remove-wins is stated and matches the product's expectation.

## 3. Replica identity

- [ ] Replica ids are globally unique, stable across restarts, and assigned to durable replicas (regions, shard owners), never to clients, requests, or ephemeral pods.
- [ ] Two nodes can never run with the same id (the merge would silently drop one node's increments).
- [ ] Decommissioning a replica has a procedure (its slot stays in the counter forever unless compacted).

## 4. Metadata growth

- [ ] Counter state is `O(replicas)` and the replica count is bounded.
- [ ] OR-Set tombstones have a garbage-collection story (causal context / stability protocol) or a documented ceiling.
- [ ] State size shipped per sync is measured; delta shipping is used if full-state is too large.
- [ ] Timestamps used by any LWW component come from a source with known skew, and the tie-break is deterministic.

## 5. Test plan: the three laws

For the concrete type, with a seeded random state generator:

- [ ] `merge(a, b) == merge(b, a)` on N random pairs (commutative).
- [ ] `merge(merge(a, b), c) == merge(a, merge(b, c))` on N random triples (associative).
- [ ] `merge(a, a) == a` on N random states (idempotent).
- [ ] K replicas merged all-to-all in shuffled orders end equal (convergence).
- [ ] One deliberately broken merge (e.g. sum instead of max) is included to prove the tests can fail.
- [ ] The global-invariant violation (e.g. two concurrent debits producing a negative balance) is reproduced in a test so nobody can later claim the type prevents it.

## 6. Operations

- [ ] Dashboards show the merged value and the per-replica contributions separately.
- [ ] A runbook describes how to reconcile a negative or impossible merged value with the business ledger.
- [ ] The design names the lesson or paper the merge semantics come from, so the next engineer can check the claim.
