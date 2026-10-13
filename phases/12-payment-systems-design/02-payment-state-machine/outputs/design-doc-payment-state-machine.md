---
name: design-doc-payment-state-machine
description: Design-document template for any object with a money-moving lifecycle (payment, refund, payout, dispute, subscription)
phase: 12
lesson: 2
tags: [state-machine, design-review, concurrency, idempotency, payments]
---

# Lifecycle Design Document: <object name>

Fill every section before the first handler is written. A reviewer should be able to answer "can X happen when the object is in state Y" from this document alone.

## 1. States

| State | Meaning | Terminal? | Who can observe it (API, webhook, dashboard) |
|-------|---------|-----------|-----------------------------------------------|
| | | | |

Rules:
- Every state is a closed enum value. No free-text status column.
- Terminal states have no outgoing transitions. List them explicitly: ___
- If two states differ only by a derived value (for example `SUCCEEDED` with and without partial refunds), say whether they are one state plus a field or two states, and why.

## 2. Events

| Event | Family (command or external fact) | Source | Carries an amount? | Deduplication key |
|-------|------------------------------------|--------|--------------------|-------------------|
| | | | | |

Rules:
- Commands come from the merchant or customer and may be refused.
- External facts (processor callbacks, bank files, network messages) arrive at least once and possibly out of order; they must be recorded or recognised as duplicates, never refused as illegal unless the table says the pair is impossible.
- Every external fact names its deduplication key (processor reference, webhook event id, file line hash).

## 3. Transition table

| From | Event | Guard | To | Side effect after save |
|------|-------|-------|----|------------------------|
| | | | | |

Rules:
- Every pair not in this table is refused with a message naming the state and the event.
- Each guard is named, has a one-line predicate, and reads only the aggregate and the event. Guards never call the network.
- Two rows with the same (from, event) must have mutually exclusive guards; write down why they are exclusive.
- Side effects (call the processor, emit a webhook) happen after the transition is saved, never before. State the recovery path if the process dies between save and side effect.

## 4. Persistence

- Transitions are append-only rows: `(object_id, version, event_id, kind, from_state, to_state, amount_minor, currency, reason, at)`.
- Primary key `(object_id, version)` provides optimistic concurrency: ___ (confirm this is how the losing writer fails).
- Unique `(object_id, event_id)` provides deduplication even if the application forgets.
- The denormalised `state` column on the parent table is a cache. Who rebuilds it when it disagrees with the log, and how often: ___

## 5. Concurrency

- Two handlers act on the same object concurrently. Which one wins, how does the loser find out, and what does the loser do (re-decide after reload, never re-apply blindly): ___
- Is any lock held across a network call? It should not be. If it is, justify it: ___

## 6. Amounts

- Every amount is integer minor units with a currency (Phase 10, Lesson 01).
- Guards that compare amounts state the inequality exactly (`<`, `<=`, `==`) and which fields they sum.
- Over-refund, over-capture, and negative or zero amounts are refused by a guard, not by handler code.

## 7. Tests the review expects to see

- [ ] Every row of the transition table is driven and lands in the stated `to` state.
- [ ] Every (state, event) pair outside the table is refused and leaves state, version, and the uncommitted list untouched. The test states the expected count.
- [ ] A redelivered external fact is a no-op by event id, including when the object has since moved on.
- [ ] Two writers from the same version: one succeeds, one gets a conflict, and the conflict message names both versions.
- [ ] Replay of a full history reproduces state, version, and every derived balance; replay of a history with a gap or a broken chain fails.
- [ ] Guard boundaries: the largest legal refund succeeds and one minor unit more is refused.

## 8. Open questions

| Question | Owner | Decision | Date |
|----------|-------|----------|------|
| | | | |
