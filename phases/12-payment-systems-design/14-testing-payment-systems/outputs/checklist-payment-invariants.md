---
name: checklist-payment-invariants
description: The invariants a payment-plus-ledger flow must hold, the property-test shape for each, and the failure modes a fake processor must be able to script
phase: 12
lesson: 14
tags: [testing, property-based, invariants, ledger, fake-processor]
---

# Payment Invariants Checklist

Use when designing the test suite for a new payment flow, or when a reconciliation break makes you suspect an invariant that was never written down. Every invariant should exist as a predicate in code, checked after every command in a property test, not only as a sentence in a design doc.

## Invariants to encode

| # | Invariant | Predicate | Catches |
|---|-----------|-----------|---------|
| 1 | Money is conserved | sum of all ledger balances == 0 | Unbalanced postings, one-legged writes, fee double counting |
| 2 | Captures bounded | captured <= authorized | Missing guard on capture, guard against last capture instead of total |
| 3 | Refunds bounded | refunded <= captured | Missing guard on refund, guard against last capture instead of total |
| 4 | Ledger agrees with payment record | balance(customer) == -(captured - refunded) | Posting skipped or duplicated relative to the state change |
| 5 | Fees never negative | balance(fees) >= 0 | Rounding of negative amounts, refund-of-fee bugs |
| 6 | Ledger agrees with processor | when not UNKNOWN: captured == processor.captured and refunded == processor.refunded | Posting on timeout, missing reconcile, reconcile that only adds |
| 7 | No operation while unresolved | state UNKNOWN rejects everything except reconcile | Double posting after an ambiguous timeout |
| 8 | Each journal entry balances | debits == credits per entry (Phase 10, Lesson 03) | Catches #1 earlier, per entry rather than per ledger |

- [ ] Each row above is a line in an `Invariants.check` function that returns a sentence naming the violation, not a boolean.
- [ ] The check runs after every command, so the violation names the command that caused it.
- [ ] Invariants that depend on external state (#6) are conditional on the resolution state, not skipped.

## Fake processor behaviours to script

- [ ] `APPROVE`: applied, success returned.
- [ ] `DECLINE`: not applied, decline returned. Service posts nothing.
- [ ] `ERROR` (5xx): not applied, exception. Service posts nothing and propagates.
- [ ] `TIMEOUT_AFTER_APPLY`: applied, response lost. Service posts nothing, marks UNKNOWN.
- [ ] `TIMEOUT_BEFORE_APPLY`: nothing applied, exception. Service posts nothing, marks UNKNOWN.
- [ ] Behaviour is selected per call from a script the test controls, never from a random number the test cannot see.
- [ ] The fake keeps its own record of the payment, so invariant #6 has something to compare against.
- [ ] A separate, small contract test against the real sandbox confirms the fake's assumptions (status codes, idempotency of retries).

## Property-test harness

- [ ] Generators are seeded; the seed is printed with every failure.
- [ ] Command generators include processor behaviours as part of each command, so a scenario is complete data.
- [ ] The generator is state-aware or at least biased toward the happy path, so sequences reach captures and refunds instead of being rejected at command one.
- [ ] Shrinking is implemented (or the library's shrinking is enabled) and the shrunk counterexample is what gets pasted into the regression suite.
- [ ] Rejected inputs (illegal in the current state) are distinguished from failures; a crash is always a failure.
- [ ] Every past counterexample lives in the suite as a named `Scenario` and runs on every build.

## Bugs worth re-introducing on a switch

A property suite that has never failed has never been shown to work. Keep a `Bugs` record with switches for:

- [ ] Over-capture (no bound on capture)
- [ ] Over-refund (no bound on refund)
- [ ] Unbalanced posting (credit without debit)
- [ ] Post on timeout
- [ ] Fee charged on refund
- [ ] Reconcile that removes postings instead of only adding

Run the suite with each switch on in CI and assert that it *fails*. A mutation that survives is a gap in the invariants or the generator.

## Review questions

- Which sequence of operations has nobody written an example for? Authorize, partial capture, partial refund, capture remainder, refund remainder is the usual gap.
- What does the service do between a timeout and the next reconcile? If the answer is "accepts new operations," invariant #7 is missing.
- What does the generator *never* produce? That is where the next incident is.
