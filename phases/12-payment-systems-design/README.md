# Phase 12: Payment Systems Design

> Correctness first, then availability, then everything else.

14 lessons, 4 complete, ~22 hours. Generated from `curriculum/curriculum.json` by `scripts/build_catalog.py`.

## Start this phase

**First lesson:** [Designing an Idempotent Payment API](01-idempotent-payment-api/)

Run the first complete lesson from the repository root:

```bash
python3 scripts/run_lesson.py phases/12-payment-systems-design/01-idempotent-payment-api --lang java
```

Keep the command, the exit code, and the test count as your first piece of evidence.

With an agent tutor: `/learn` (Claude Code), `learn` (Codex), or `Use learn to teach the next lesson.` The tutor reads your language from `LEARNING.md`.

## Lessons

| # | Lesson | Type | Time | Status | Hook |
|:---:|--------|:----:|:----:|:------:|------|
| 01 | [Designing an Idempotent Payment API](01-idempotent-payment-api/) | Build | ~120 min | ✅ | Idempotency keys, request fingerprints, and concurrent duplicates. |
| 02 | [The Payment State Machine](02-payment-state-machine/) | Build | ~120 min | ✅ | Every transition is explicit, persisted, and reversible or terminal. |
| 03 | [Payment Orchestration Across Processors](03-payment-orchestration/) | Build | ~120 min | ⬚ | A saga with real compensations and a dead-letter path. |
| 04 | [Smart Routing, Cascading, and Retry Policies](04-smart-routing-and-retries/) | Build | ~90 min | ⬚ | Which processor, which retry, and when to stop. |
| 05 | [Webhooks: Signing, Delivery, Ordering, and Replay](05-webhooks-and-callbacks/) | Build | ~120 min | ✅ | HMAC signatures, at-least-once delivery, and consumers that dedupe. |
| 06 | [Refunds, Partial Refunds, and Refund Windows](06-refunds-and-partial-refunds/) | Build | ~75 min | ⬚ | Refund more than captured and the ledger must say no. |
| 07 | [Disputes and Chargebacks: The Lifecycle and the Evidence](07-disputes-and-chargebacks/) | Build | ~90 min | ⬚ | A state machine that runs for 120 days. |
| 08 | [Payouts, Balances, and Negative Balance Handling](08-payouts-and-balance-management/) | Build | ~90 min | ⬚ | Pay the merchant out of funds that have actually settled. |
| 09 | [Limits, Velocity Controls, and Exposure](09-limits-and-velocity-controls/) | Build | ~75 min | ⬚ | Per-card, per-merchant, per-day, in a sliding window. |
| 10 | [Payment Method Abstraction and Processor Adapters](10-payment-method-abstraction/) | Build | ~90 min | ⬚ | One interface, nine rails, no leaking of processor quirks. |
| 11 | [Recurring Payments, Subscriptions, and Dunning](11-recurring-and-subscriptions/) | Build | ~90 min | ⬚ | Billing cycles, proration, and the retry schedule that recovers revenue. |
| 12 | [Currency Conversion at Checkout and DCC](12-currency-conversion-at-checkout/) | Build | ~60 min | ⬚ | Who bears FX risk and when the rate is locked. |
| 13 | [API Design for Money: Versioning, Pagination, and Error Semantics](13-api-design-for-money/) | Learn | ~75 min | ⬚ | A 500 on a payment is ambiguous; design so clients can recover. |
| 14 | [Testing Payment Systems: Simulators, Property Tests, and Invariants](14-testing-payment-systems/) | Build | ~120 min | ✅ | Money is conserved; let the computer try to break that. |

Browse the [full curriculum](../../README.md#contents) or the [roadmap](../../ROADMAP.md).
