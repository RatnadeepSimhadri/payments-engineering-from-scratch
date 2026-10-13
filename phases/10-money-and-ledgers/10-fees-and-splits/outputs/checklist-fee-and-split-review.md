---
name: checklist-fee-and-split-review
description: Review checklist for fee schedules, marketplace splits, discounts, tax-inclusive pricing, and payout rules
phase: 10
lesson: 10
tags: [fees, splits, marketplace, payouts, allocation, review]
---

# Fee and Split Review Checklist

Use on any pull request that changes pricing, commission, discount, tax, referral, or payout logic.

## Fee schedules

- [ ] Percentages are stored as exact rationals (basis points or numerator/denominator), never as `double`.
- [ ] The rounding mode for the percentage part is named in the schedule, not implied by a library default.
- [ ] The order of operations is explicit and tested: percent (rounded once), plus fixed, clamp to min/max, cap at the base.
- [ ] Minimum and maximum are in the same currency as the base; a mismatch fails loudly.
- [ ] Behaviour when the minimum exceeds the base is a documented policy (cap at base, or reject the sale), with a test.

## Splits

- [ ] Every proportional part (discount shares, tax from tax-inclusive prices, revenue shares) is computed with largest-remainder allocation.
- [ ] Each split has exactly one remainder part (usually the seller net); no two parts of the same split are rounded independently.
- [ ] A test asserts `sum(parts) == whole` per line and per order, and that no part is negative.
- [ ] A property test runs thousands of random orders (random prices, line counts, discounts, including one-cent lines and 100% discounts).
- [ ] Ties in allocation break deterministically (by line order), so recomputation in another service gives identical parts.

## Tax

- [ ] Tax-inclusive prices are split in the ratio `100 : rate`, not by multiplying the gross by the rate.
- [ ] Per-line versus per-invoice tax rounding matches the tax invoice the customer receives and the jurisdiction's rule.
- [ ] Collected tax is credited to a tax payable account, never to seller payables or revenue.

## Ledger

- [ ] The split posts as one balanced entry: one receivable debit, one credit per party, aggregated per party.
- [ ] Processor fees are a separate cost (expense against receivable), not deducted from a party's share by accident.
- [ ] Line-level split detail is persisted for statements and refunds even though the ledger aggregates per party.

## Payouts

- [ ] `available == reserve + fee + net + carriedOver` in every branch, with tests for each branch.
- [ ] Reserve rounding direction is deliberate (usually UP) and the reserve is a liability to the seller, not revenue.
- [ ] Negative balances are never paid out; they carry over and are netted or collected.
- [ ] Minimum payout threshold carries the whole balance over rather than paying part of it.

## Refunds of split orders

- [ ] A refund reverses each part in proportion to what remains on that line (see Lesson 06), never more than was captured.
- [ ] Whether the platform refunds its commission and who absorbs the processor fee are explicit policies with tests.
