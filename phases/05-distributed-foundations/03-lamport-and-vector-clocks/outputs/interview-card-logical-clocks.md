---
name: interview-card-logical-clocks
description: One-page card on Lamport and vector clocks for design reviews and interviews about event ordering across payment services
phase: 5
lesson: 3
tags: [clocks, causality, ordering, vector-clocks, interview]
---

# Logical Clocks Interview Card

Use when a design orders events from more than one service, or when someone proposes "sort by timestamp".

## Happens-before in one breath

`a -> b` if `a` precedes `b` on the same process, or `a` is a send and `b` its receive, or transitively. Otherwise `a || b` (concurrent). Concurrent means "no information flowed", not "same instant".

## Lamport clock: three rules

| Rule | Action |
|------|--------|
| Tick (local event) | `C := C + 1` |
| Send | tick, attach `C` to the message |
| Receive stamp `t` | `C := max(C, t) + 1` |

- Guarantees: `a -> b` implies `C(a) < C(b)`. The converse is false.
- Total order: sort by `(C, nodeId)`. Consistent with causality; arbitrary but deterministic for concurrent pairs.
- Cost: 8 bytes per message, O(1) everything. Cannot detect concurrency.

## Vector clock: four outcomes

Merge on receive: componentwise max, then increment own slot. Compare componentwise:

| Outcome | Condition | Meaning |
|---------|-----------|---------|
| BEFORE | all `<=`, at least one `<` | `a` could have caused `b` |
| AFTER | all `>=`, at least one `>` | `b` could have caused `a` |
| EQUAL | all `=` | same event or same observed state |
| CONCURRENT | some `<` and some `>` | conflict: neither saw the other |

- Guarantees: `a -> b` if and only if `V(a) < V(b)`.
- Cost: `8n` bytes per message, O(n) compare; needs a known membership.
- Merge is commutative, associative, idempotent: safe under reorder, regroup, redelivery.

## The worked example to draw on the whiteboard

Nodes `payments` (0), `refunds` (1), `ledger` (2). Refund issued before the capture notice arrives.

| Event | Lamport | Vector |
|-------|---------|--------|
| payments: post captured 49.99 USD | 1 | `[1,0,0]` |
| refunds: post refunded -49.99 USD | 1 | `[0,1,0]` |
| ledger: recv captured | 3 | `[2,0,1]` |
| ledger: recv refunded | 4 | `[2,2,2]` |

Lamport tie-break says capture first, a convention. Vector says `[1,0,0] || [0,1,0]`: concurrent, route to a conflict handler. In the healthy flow the refund carries `[3,2,0]`, which dominates `[1,0,0]`: causal, safe to apply.

## Six questions a reviewer will ask

1. **How do you know the refund came after the capture?** Answer with causality (a vector or an explicit dependency id carried on the refund), never with host timestamps.
2. **What happens when NTP steps a clock back?** Logical clocks are unaffected; any wall-clock ordering in the design breaks.
3. **Who are the processes in your vector?** Services, not clients. Per-client vectors grow without bound (Dynamo's problem); use version vectors keyed by replica id.
4. **What happens when a node joins?** Fixed arrays must reject mismatched sizes; maps treat a missing entry as zero.
5. **What do you do with a concurrent pair?** Business rule, decided in advance: net, reject, or page an operator. The clock only finds the conflict.
6. **Do you need concurrency detection or just one agreed order?** One agreed order: `(counter, nodeId)` or a consensus log. Detection: vector clock.

## Red flags in a design doc

- [ ] "Events are ordered by `created_at`" across services.
- [ ] Wall clock used as the primary sort key rather than only as a tie-break.
- [ ] A vector clock stored by mutable reference and logged later.
- [ ] Last-writer-wins on a ledger or balance record.
- [ ] No stated membership for the vector clock.
