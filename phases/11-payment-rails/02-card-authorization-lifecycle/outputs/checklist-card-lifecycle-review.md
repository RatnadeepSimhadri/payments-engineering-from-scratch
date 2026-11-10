---
name: checklist-card-lifecycle-review
description: Review checklist for any code that authorizes, increments, captures, voids, expires, or refunds a card transaction
phase: 11
lesson: 2
tags: [cards, authorization, capture, refund, state-machine, review]
---

# Card Lifecycle Review Checklist

Use on every pull request that creates or changes a transition on a card transaction: checkout, order management, shipping, the support console, and batch jobs. One unchecked box is a reason to request changes.

## One choke point

- [ ] Every state change goes through a single `transition(action, amount)` method. No code path assigns `state` directly, including tests and migration scripts.
- [ ] The legal-move table is data (a map from state to the set of allowed actions), not a chain of `if` statements spread across handlers.
- [ ] Every illegal transition raises a specific error (`IllegalTransitionException`) that names the current state and the attempted action. The error is not swallowed and converted into a different action (a refund request on `AUTHORIZED` is never silently turned into a void).
- [ ] Terminal states (`VOIDED`, `EXPIRED`, `REFUNDED`) accept no actions. A second void is an error, not an idempotent success.

## The two amount invariants

- [ ] `captured <= ceiling(authorized)` is checked before any event is written. The ceiling is `authorized` unless the merchant category has a documented over-capture tolerance, and the tolerance is computed with `Rounding.DOWN`.
- [ ] `refunded <= captured` is checked before any event is written. An over-refund is rejected with `LimitExceededException`, never trimmed to the remaining amount.
- [ ] Every amount is a `Money` (integer minor units plus currency). A capture or refund in a different currency from the authorization is a currency-mismatch error.
- [ ] Zero and negative amounts are rejected for every action that carries an amount.
- [ ] `remainingAuthorization()` and `netSettled()` are derived from the running totals, never stored as separate mutable fields that can drift.

## Time

- [ ] The transaction reads time from an injected `Clock`. No `Instant.now()`, `time.time()`, or `time.Now()` inside the state machine.
- [ ] Expiry is applied lazily before every merchant-initiated action (`expireIfDue`), so a late capture fails as an illegal transition from `EXPIRED` with the expiry recorded as its own event.
- [ ] Expiry of a `PARTIALLY_CAPTURED` hold lands in `CAPTURED`, not `EXPIRED`: captured funds must stay refundable, so no terminal state ever holds captured money.
- [ ] The authorization validity period comes from a `LifecyclePolicy` keyed by merchant category, not a constant in the code.
- [ ] Incremental authorizations extend the ceiling but do not reset the validity window unless the policy says they do, and the test suite has a case that pins this down.

## The event log

- [ ] Every successful transition appends exactly one `TransactionEvent` with a monotonically increasing sequence number, the action, the amount, the from-state, the to-state, and the clock timestamp.
- [ ] A rejected transition writes nothing. State, totals, and the log are untouched after any exception.
- [ ] Replaying the log reproduces the current state and totals (Exercise 2). The replay rejects a log whose sequence numbers are not contiguous.
- [ ] The processor's reference for each transition (authorization code, capture ID, refund ID) is stored on the event so a dispute can be traced to a network message.

## Integration points

- [ ] Order management calls `closeCapture` when the last shipment goes out, so the uncaptured remainder is released instead of waiting for expiry.
- [ ] The support console's refund form defaults to the refundable amount (`captured - refunded`) and the server re-checks the invariant regardless of what the form sent.
- [ ] Batch jobs that capture overnight check expiry first and surface the list of expired authorizations to a human rather than retrying them.
- [ ] Partial captures and increments are only enabled for merchant categories where the processor and network permit them; the policy object makes that explicit.

## Tests present

- [ ] One test per illegal edge the lesson calls out: void after capture, increment after partial capture, refund before capture, any action from a terminal state.
- [ ] A capture that would exceed the ceiling by one minor unit, and one exactly at the ceiling.
- [ ] A refund that would exceed captured by one minor unit, and one that brings refunded exactly to captured (state becomes `REFUNDED`).
- [ ] A capture attempted one second after expiry, with the expiry event visible in the log.
- [ ] A rejected transition followed by an assertion that the event count did not change.
