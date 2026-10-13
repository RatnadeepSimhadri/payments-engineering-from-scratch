---
name: checklist-card-fee-engine
description: Review checklist for any system that computes, reports, or reconciles card processing fees (interchange, scheme fees, acquirer pricing)
phase: 11
lesson: 7
tags: [interchange, scheme-fees, pricing, reconciliation, checklist]
---

# Checklist: Card Fee Engine

Use this when designing a merchant pricing system, reviewing a fee reconciliation job, or deciding between blended and IC++ pricing. Every item is a yes/no question; a "no" needs a written reason.

## Numbers

- [ ] Every amount is integer minor units with a currency (Phase 10, Lesson 01). No `double`, `float`, or JSON number with a decimal point anywhere in the fee path.
- [ ] Percentage rates are stored as exact integers (parts per million or basis points with enough precision for the finest published rate, e.g. 0.0013%).
- [ ] Rates are parsed from their published string form, never via a floating-point literal.
- [ ] Each fee component is rounded exactly once, with a named rounding mode, and the rounding mode is documented next to the rate source.
- [ ] Multiplications that can overflow 64 bits fail loudly (`Math.multiplyExact`, checked arithmetic) instead of wrapping.

## The split invariant

- [ ] For every transaction, interchange + scheme fees + acquirer margin + merchant net == gross, enforced in code (constructor or database constraint), not only in a test.
- [ ] The last component is derived by subtraction, not rounded independently.
- [ ] A negative acquirer margin is representable, recorded, and alerted on rather than clamped to zero.
- [ ] Aggregate fees (monthly scheme fees, statement fees) are attributed to transactions with largest-remainder allocation, and the attributed parts sum to the invoice.

## The rate table

- [ ] The table has an owner, a source document (scheme schedule and version date), and an effective-from date per row.
- [ ] Regulatory caps (Regulation II for US regulated debit; IFR for EEA consumer cards; any national caps) are checked when the table is loaded.
- [ ] A missing row is an error routed to an alert, not a silent default rate.
- [ ] The table is versioned so that a transaction can be re-quoted with the rates in force on its clearing date.
- [ ] Rate changes (April and October for the major US schedules) are loaded and tested before their effective date.

## Inputs to the lookup

- [ ] Card type and issuer country come from a maintained BIN table (Phase 11, Lesson 04), refreshed on the scheme's schedule.
- [ ] Region is derived from issuer country versus acquirer country, not from the merchant's or cardholder's address.
- [ ] Channel comes from the authorization's POS entry mode and authentication result, not from which API endpoint was called.

## Downgrades

- [ ] Each quote records the interchange category it expected.
- [ ] A reconciliation job compares the expected category with the category on the acquirer's interchange report and reports the cost difference per cause.
- [ ] Known downgrade causes are monitored: clearing later than the qualification window, missing Level 2/3 data on commercial cards, missing AVS/CVV/3DS results, clearing amount outside tolerance.

## Pricing model

- [ ] For blended pricing: the expected card mix was measured, and the margin per card type is reported monthly.
- [ ] For IC++ pricing: the merchant statement shows interchange, scheme fees, and markup separately per card category and brand (required in the EEA by IFR Article 9).
- [ ] Surcharging, if offered, follows scheme rules and local law (prohibited on IFR-capped consumer cards in the EEA by PSD2 Article 62(4)).
- [ ] Refund fee treatment (interchange returned or not, markup refunded or not) is specified per pricing model.
