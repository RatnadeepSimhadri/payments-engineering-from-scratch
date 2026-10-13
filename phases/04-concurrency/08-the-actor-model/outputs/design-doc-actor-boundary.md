---
name: design-doc-actor-boundary
description: Design-review template for proposing that a component (an account, a session, a payment) be modelled as an actor
phase: 4
lesson: 8
tags: [concurrency, actor-model, design-review, supervision]
---

# Design Review: Making `<X>` an Actor

Fill in every section. A reviewer should be able to reject the proposal from this document alone if the model does not fit.

## 1. Identity and state

- **Actor key:** what one instance corresponds to (one account id, one payment id, one merchant session).
- **Private state:** list every field. For each, confirm no other component reads or writes it directly.
- **State size and count:** how many instances can be live at once, and how much memory each holds. (A million account actors at 1 KB each is 1 GB.)

## 2. Messages

| Message | Direction | Fields (all immutable) | Reply, if any | Check-then-act? |
|---------|-----------|------------------------|---------------|-----------------|
| | | | | |

- Every message is a value type (`record`, frozen dataclass, struct by value). No message carries a mutable collection, a stream, a connection, or a reference to another actor's state.
- Every request that expects an answer carries `replyTo` and a correlation id.
- Mark each message that performs a check-then-act (debit if sufficient, claim if unclaimed). These are the operations the actor model makes atomic for free; list them so the reviewer sees the benefit.

## 3. Multi-actor operations

For each operation that involves more than one actor (transfer, settlement, split):

- **Protocol:** the message sequence, as a sequence diagram.
- **Intermediate states:** what is observable between steps (funds in transit, a reservation held). State explicitly that this is acceptable, or how it is hidden.
- **Failure at each step:** what happens if step k fails or its reply never arrives. Name the compensation or the timeout.
- **Idempotency:** what prevents a retried message from being applied twice.

## 4. Supervision

- **Strategy:** RESUME / RESTART / STOP, per actor type.
- **For RESTART:** where state is recovered from (journal, database, snapshot). "The factory re-creates it" is only acceptable if the state is derivable. An in-memory balance recreated from an opening amount is a money-losing bug.
- **For STOP:** who notices, and what happens to messages already in the mailbox (dead letters) and to senders that keep sending.
- **Failure visibility:** how failures are logged and counted; which alert fires when an actor fails more than N times per minute.

## 5. Mailbox and back-pressure

- **Bound:** the mailbox capacity and the behaviour when full (reject, drop oldest, block the sender). Unbounded is not an answer for anything a customer or a partner can flood.
- **Throughput:** the dispatcher batch size, and why one busy instance cannot starve the others.
- **Slow consumers:** what happens when this actor's dependencies (a processor, a database) slow down. Does the mailbox grow, or does something upstream stop sending?

## 6. Scheduling and threads

- **Dispatcher:** which thread pool, its size, whether blocking calls are allowed inside `receive` (they should not be on a shared dispatcher; use async replies or a dedicated pool).
- **Visibility:** confirm no state is shared outside the actor; if a metric or cache is, state how it is synchronised.

## 7. Testing

- **Deterministic scheduler:** the test harness runs the actor system single-threaded with a seed. List the invariants checked across seeds (conservation, non-negativity, one reply per request, nothing in flight at idle).
- **Threaded test:** the stress test on a real pool, asserting the same invariants plus `maxConcurrentReceives == 1`.
- **Fault injection:** a `Crash`-style message or a throwing dependency, exercising each supervision strategy.

## 8. Alternatives considered

- A lock per key (Lesson 02) with a lock order (Lesson 07): why not.
- A database transaction: why not, or why this actor fronts one.
- A partitioned stream processor (one partition per key): how this differs, if at all.

## Reviewer's checklist

- [ ] No field of the actor is reachable from outside it.
- [ ] Every message is immutable.
- [ ] Every multi-actor operation has a named failure behaviour for every step.
- [ ] RESTART, if used, recovers state from a durable source.
- [ ] The mailbox is bounded or the sender is otherwise throttled.
- [ ] The deterministic test exists and runs under multiple seeds in CI.
