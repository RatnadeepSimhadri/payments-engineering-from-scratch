---
name: checklist-refunds
description: Design-review checklist for refund and partial-refund implementations (invariant, atomic reservation, journal entries, fee reversal rounding, windows, idempotency, reconciliation)
phase: 12
lesson: 6
tags: [refunds, ledger, idempotency, concurrency, fees, design-review]
---

# Refunds Design-Review Checklist

## The invariant

- [ ] Written down: for every capture, `succeeded + pending <= captured`, and remaining = `captured - succeeded - pending`.
- [ ] Pending refunds consume balance at request time, not at confirmation.
- [ ] Enforced in at least two places: the application check and a database constraint (`CHECK` or ledger-level balance limit).

## Atomicity

- [ ] The over-refund check and the reservation are one statement or one transaction holding a row lock (conditional `UPDATE ... WHERE remaining >= :amount`).
- [ ] There is a concurrency test: N parallel requests against one capture, asserting exactly floor(captured / amount) succeed.
- [ ] The refund record and the reservation journal entry are written in the same transaction.

## Order of checks

1. Idempotency key: same key and same request returns the existing refund; same key and different request is a conflict.
2. Shape: amount positive, currency equals the capture's currency.
3. Window: today is on or before capture date + window days.
4. Balance: amount <= remaining.
5. Dispute hold: the capture is not in an open dispute.

Every check runs before the first write. A refused request leaves no row and no journal entry.

## Journal entries

| Event | Debit | Credit | Posted by |
|-------|-------|--------|-----------|
| Request accepted | merchant balance | refunds payable | refund API |
| Processor confirms | refunds payable (+ fee revenue for the fee share) | scheme settlement (+ merchant balance for the fee share) | webhook consumer |
| Processor rejects | reversal of the request entry | | webhook consumer |

- [ ] A failed refund is a reversal entry, never a delete or an edit.
- [ ] A redelivered confirmation is a no-op, deduplicated by refund id or processor event id.
- [ ] A confirmation for a refund recorded as failed (or the reverse) raises an alert for a human.

## Fees

- [ ] The contract's fee policy is recorded per processor: none, proportional, variable-part only.
- [ ] Proportional shares are computed cumulatively: `floor(fee x refunded_after / captured) - fee_returned_so_far`.
- [ ] Test: a capture refunded in many small pieces returns exactly the fee in total.
- [ ] Under a no-return policy, the merchant's balance may go negative after a full refund; the payout system handles it.

## Window

- [ ] The window length is configuration, per payment method if processors differ.
- [ ] The boundary day is inside the window; there is a test for day N and day N+1.
- [ ] A refund requested inside the window may settle after it closes.
- [ ] Out-of-window requests are routed to a separate, more controlled flow (bank payout or store credit), never silently to an unreferenced credit.

## Reconciliation

- [ ] Daily: `refunds_payable` balance equals the sum of pending refund amounts.
- [ ] Daily: processor refund report matches succeeded refunds by processor reference and amount.
- [ ] Alert on any refund pending longer than the processor's documented maximum.
