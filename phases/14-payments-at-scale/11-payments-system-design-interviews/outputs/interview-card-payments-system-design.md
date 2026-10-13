---
name: interview-card-payments-system-design
description: One-page card for a 45-minute payments system design interview, usable from either side of the table
phase: 14
lesson: 11
tags: [interview, system-design, payments, rubric]
---

# Payments System Design Interview Card

## The clock

| Minutes | Candidate does | Interviewer does |
|---------|----------------|------------------|
| 0 to 5 | Asks the eight questions; writes answers on the board | Answers only what is asked; notes what was skipped |
| 5 to 15 | Draws the standard architecture; names the source of truth; draws the state machine | Says nothing unless the candidate is lost |
| 15 to 40 | Answers probes: mechanism, invariant, failure prevented | Asks one level deeper after every answer |
| 40 to 45 | States what to build first and what to cut from v1 | Scores on the rubric immediately afterwards |

## The eight clarifying questions

1. Who moves the money: external processor, bank rail, or our own ledger?
2. Card, bank transfer, or wallet-to-wallet?
3. Which operations: charge, refund, partial capture, dispute, payout?
4. Volume: average, peak, largest single merchant's share?
5. Synchronous approval or asynchronous with notification?
6. Multi-currency?
7. Regulatory scope: PCI, sanctions, data residency?
8. Which is worse for this business: a duplicate charge or a missed one?

## The sentence to say while drawing

"The payment store is the only source of truth; the API holds nothing; the processor is told nothing until the store has a durable record; everything downstream learns through the outbox."

## The five probes

| Probe | Mechanism to name | Invariant to state | Failure to describe |
|-------|-------------------|--------------------|---------------------|
| Idempotency | unique index on (merchant, key) in the payment transaction; body hash; same processor key on retry | one key, one payment, one authorization | retry storm during a slowdown doubles charges |
| Balance consistency | double-entry journal; derived balances; per-account serialization; available = posted minus holds | trial balance zero; sum of balances equals funding | two withdrawals read 100, both take 80 |
| Failure handling | `UNKNOWN` state; resolver by processor key; void as fallback; timeout from p99 | never terminal while the processor disagrees | "declined" for a charged card, customer pays twice |
| Reconciliation | three-way match on reference then amount/date; typed breaks that age and page | matched plus breaks equals both inputs | internal ledger clean while processor has duplicates |
| Scale | Little's law for the slow case; outbox partitioned by payment; degradation ladder | slow dependency degrades its own traffic only | processor slowdown exhausts edge threads |

## Answer shape (under ninety seconds)

Mechanism (a noun) then invariant (a sentence true at every instant) then failure prevented (a story with a number). Stop. Offer the trade-off only if asked.

## Six weak answers and their strong versions

| Weak | Strong |
|------|--------|
| "The queue is exactly-once" | "At-least-once; the consumer's unique index makes the effect once" |
| "We retry until it works" | "Same key, capped, backoff; then `UNKNOWN` and resolve by query" |
| "Timeout returns an error" | "Timeout is unknown, not failed" |
| "Balance is a column" | "Balance is a fold over an immutable journal; the column is a cache with an LSN" |
| "Distributed transaction across services" | "One truth; downstream via outbox; reconciliation proves agreement" |
| "We reconcile when there's a complaint" | "Daily three-way match with typed breaks; not correct until clean" |

## Rubric, compressed

| Criterion | Solid means | Exemplary adds |
|-----------|-------------|----------------|
| Questions | asks who moves the money, operations, volume | asks duplicate-versus-missed cost and uses it |
| Architecture | source of truth named; processor after durable write | outbox drawn; explains stateless edge in one sentence |
| Idempotency | same transaction; hash; replay vs conflict | concurrent duplicate; processor key; scope |
| Money | integer minor units; double-entry; derived balances | per-currency trial balance; holds; reversal; names the race |
| Failures | failed vs unknown; resolver | timeout from p99; breaker only for unsent; void fallback |
| Reconciliation | daily match; break types | three-way; ageing and ownership; "balanced proves nothing" |
| Scale | peak rate; Little's law; slow case | processor rate limit first; ladder never starves convergence |
| Communication | moves on at 15; answers the probe asked | trade-offs as decisions; what to build first |

## Interviewer's follow-ups

After any answer: "Where does that live?" / "What if that fails?" / "How would you know?"

Last five minutes: "What do you build first, and what do you cut from v1?" Cutting reconciliation is a red flag.
