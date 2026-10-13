---
name: design-doc-event-sourced-aggregate
description: One-page design-review template for proposing an event-sourced aggregate, from event list to snapshot policy
phase: 8
lesson: 5
tags: [event-sourcing, aggregate, design-review, concurrency, snapshots]
---

# Event-Sourced Aggregate: Design Review Template

Fill this in before writing code. A reviewer should be able to reject the design from this page alone.

## 1. The aggregate

- **Name and identity:** (e.g. `Account`, keyed by `account_id`)
- **Why one stream:** which invariant requires all of these events to be ordered together? If two groups of events never share an invariant, they are two aggregates.
- **Expected stream length:** events per aggregate per year, p50 and p99. This drives the snapshot policy in section 5.

## 2. Events (facts, past tense)

| Event | Fields | Emitted by command | Notes |
|-------|--------|--------------------|-------|
| `Opened` | account_id, currency | `OpenAccount` | first event of every stream |
| | | | |

Checks:

- [ ] Every event name is past tense and describes something that happened, not something to do.
- [ ] No event carries derived state (no `new_balance` field). Derived values are recomputed by `apply`.
- [ ] No event carries its own version or stream id; the store supplies both.
- [ ] Amounts are integer minor units with a currency (Phase 10, Lesson 01). No floats.
- [ ] Each event is small enough to be understood by a reader who sees only that event and the previous state.

## 3. Commands and the rules `decide` enforces

| Command | Preconditions checked in `decide` | Events emitted | Rejection reason text |
|---------|-----------------------------------|----------------|------------------------|
| `Withdraw` | open; amount > 0; same currency; amount <= available | `Withdrawn` | "insufficient available funds" |
| | | | |

Checks:

- [ ] Every business rule appears in this table and nowhere else. `apply` contains no `if` that can refuse.
- [ ] Idempotency: a retried command with the same idempotency key (e.g. hold id) is rejected or deduplicated, never applied twice.
- [ ] Invariants are stated against the right quantity (available vs ledger balance).

## 4. Concurrency: the expected-version strategy

- **Store:** (EventStoreDB / KurrentDB, relational table, Kafka partition, custom log)
- **How conditional append is implemented:** (`expectedRevision`, unique constraint on `(stream_id, version)`, compare-and-append on end offset, single writer per key)
- **On conflict:** reload, re-decide, retry up to N times; then surface to the caller as a retryable error.
- [ ] A test exists in which two writers load the same version and exactly one succeeds.
- [ ] If the store cannot do a conditional append (plain Kafka), the single-writer guarantee is documented and enforced by partition ownership.

## 5. Snapshots

- **Policy:** every N events / every T minutes / when load exceeds X ms. N = ___
- **Invalidated when:** `apply` changes meaning, or the state type changes shape. Snapshots are a cache; deleting all of them must be a safe operation.
- [ ] A test asserts `load(snapshot + tail) == replay(all)`.
- [ ] Snapshot writes are outside the append transaction, or their failure is tolerated.

## 6. Reads

- Which queries hit a projection (Lesson 06) and which, if any, replay the aggregate? Replaying for a human-facing read is a red flag.
- Projection lag tolerance: ___ ms.

## 7. Schema evolution

- How will a new field on an existing event be read from old events (default value, upcaster)?
- Who owns the upcaster, and is there a test that replays a recorded production stream from a year ago?

## Reviewer questions

1. Which invariant would break if this were two aggregates? If none, split it.
2. Show me the test where two writers conflict.
3. What is the p99 stream length in three years, and what does `load` cost then without a snapshot?
4. Which event, if its meaning changed, would force every snapshot to be deleted? Is that acceptable?
5. Is there any `if` in `apply`? Why?
