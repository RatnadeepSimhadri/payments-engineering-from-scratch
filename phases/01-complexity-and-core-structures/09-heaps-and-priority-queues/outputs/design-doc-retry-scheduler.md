---
name: design-doc-retry-scheduler
description: Design-review template for any component that schedules work by time (retry queues, timeout managers, settlement windows)
phase: 1
lesson: 9
tags: [heap, scheduler, retries, backoff, design-review]
---

# Time-Ordered Scheduler: Design Review Template

Fill in every section before the review. A blank section is a finding.

## 1. What is scheduled

- Item type and unique key (e.g. `paymentId`): ______
- Expected steady-state count: ______  Peak count after a dependency outage: ______
- Resolution needed (ms / s / minute): ______
- Does an item's due time ever change after scheduling? (yes: you need decrease-key or lazy deletion) ______

## 2. Data structure and complexity

| Operation | Structure chosen | Cost | Cost at peak count |
|---|---|---|---|
| schedule | | | |
| poll due | | | |
| re-prioritise one item | | | |
| re-prioritise all items | | | |
| cancel | | | |

- [ ] If re-prioritisation is needed, the structure supports it in O(log n) (indexed heap, `heap.Fix`, sorted set), or lazy deletion is used and the stale-entry ratio is bounded.
- [ ] If count is in the millions and resolution is coarse, a timing wheel was considered and the decision is recorded.
- [ ] If the scheduler must survive a restart, the durable store (sorted set, table with an index on `due_at`) is named and the in-memory heap is a cache of it, not the source of truth.

## 3. Time

- [ ] The scheduler takes a `Clock` and never calls the system clock directly; tests use a fake clock and never sleep.
- [ ] Priorities are absolute due times, not relative delays, so a paused scheduler releases the right items on resume.
- [ ] Priorities are immutable once in the structure; the only way to change one is through the structure's own re-prioritise operation.
- [ ] Clock skew between schedulers (if more than one instance) is bounded and the bound is documented.

## 4. Backoff policy

- Formula (base, multiplier, cap): ______
- Jitter (none / full / equal) and the seeded source used in tests: ______
- Maximum attempts and what happens after (dead-letter, alert, give up): ______
- [ ] The shift or multiplication cannot overflow for any attempt count the system can reach.
- [ ] After a dependency outage, released retries are rate-limited so they do not recreate the outage (thundering herd).

## 5. Ordering guarantees

- [ ] Ties (same due time) have a defined order (FIFO by sequence number) and a test that proves it; "arbitrary" is not accepted for anything customer-facing.
- [ ] Re-prioritised items state whether they keep their original sequence rank or take a new one.
- [ ] Fairness across tenants/merchants is either guaranteed by construction or explicitly out of scope.

## 6. Failure modes to test

- [ ] 100,000 items scheduled within one second: schedule and first poll both complete within the latency budget.
- [ ] Re-prioritise every pending item: completes in O(n log n), not O(n^2).
- [ ] Poll exactly at the due millisecond returns the item; one millisecond earlier does not.
- [ ] Duplicate key scheduled twice: rejected or coalesced, never run twice.
- [ ] Structure invariant (heap order, index consistency) checked after a seeded random sequence of operations.
- [ ] Process restart mid-schedule: no item is lost and no item runs twice (if durability is in scope).

## 7. Observability

- Gauge: pending count. Gauge: oldest overdue item age. Counter: items released per poll.
- Alert when oldest overdue age exceeds ______ (the scheduler is falling behind).

## Sign-off

Reviewer: ______  Date: ______  Open findings: ______
