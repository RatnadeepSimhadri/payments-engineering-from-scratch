---
name: design-doc-multi-currency-ledger
description: Design-review template for adding a currency or a cross-currency flow to a payments ledger
phase: 10
lesson: 9
tags: [ledger, fx, multi-currency, revaluation, design-review]
---

# Design Doc: Adding <CURRENCY> / <CROSS-CURRENCY FLOW> to the Ledger

Fill in every section. "N/A" needs a sentence explaining why.

## 1. Summary

- Flow: <e.g. US buyers paying EU sellers; EUR payouts funded from USD settlement>
- New currencies: <codes, ISO 4217 exponents>
- Functional (reporting) currency of the entity that owns these balances: <code>
- Expected volume and peak open position per currency: <numbers>

## 2. Account plan

| Account id | Name | Type | Currency | Clearing? | Purpose |
|------------|------|------|----------|-----------|---------|
| 1900-<CUR> | FX Clearing <CUR> | Asset | <CUR> | yes | Every exchange into or out of <CUR> |
| 1950-<CUR> | FX Revaluation Adjustment <CUR> | Asset | <FUNCTIONAL> | no | Cumulative revaluation of the <CUR> position |
| 4900 | Unrealized FX Gain/Loss | Revenue | <FUNCTIONAL> | no | Revaluation results |
| 4910 | Realized FX Gain/Loss | Revenue | <FUNCTIONAL> | no | Results when positions close |
| ... | | | | | |

- [ ] Every account has exactly one currency, fixed at creation.
- [ ] No entry type in this flow posts to accounts of two currencies.

## 3. Entries per business event

For each event (payment, refund, payout, conversion, fee), write the entries per currency:

```text
<event id>/<CUR A>: Dr ... Cr ...
<event id>/<CUR B>: Dr ... Cr ...
rate: 1 <base> = <num>/<den> <quote>, source <...>, as of <...>
```

- [ ] Each entry balances in its own currency without a rate.
- [ ] Entries for one event are posted atomically as one transaction with one idempotency key.

## 4. Rates

- Source for dealing rates (conversions that move money): <provider, endpoint, refresh>
- Source for closing rates (revaluation): <e.g. ECB reference at 16:00 CET>
- Representation: exact rational, stored with source and timestamp on every transaction.
- Maximum acceptable rate age at conversion time: <duration>; behaviour when stale: <reject / fallback>
- Spread or markup policy and where it is booked: <account>

## 5. Rounding

- Rounding mode at conversion: <HALF_EVEN unless a rule requires otherwise; cite it>
- Exponent differences handled: <e.g. JPY 0, KWD 3 test cases>
- Where rounding effects appear: in basis and realized gain/loss (no suspense account).

## 6. Positions, revaluation, realization

- Position definition per currency: net debit of non-clearing accounts.
- Basis tracking: <how; must route through functional currency or supply per-leg functional rates>
- Revaluation schedule: <daily / month-end>, reversal policy: <unwind on realization / auto-reverse next period>
- Partial realization method: <average cost / FIFO / N/A because positions always close fully>

## 7. Invariants and monitoring

- [ ] Every sub-ledger nets to zero (alert on any non-zero).
- [ ] netDebit(1900-<FUNCTIONAL>) == sum of open bases (alert on drift).
- [ ] After revaluation, carrying value == position at closing rate.
- [ ] Open position per currency within treasury limit <amount> (alert, and who is paged).

## 8. Reconciliation

- Per-currency bank and processor statements reconciled against the matching sub-ledger, in that currency, with no rate.
- Processor conversion records reconciled against our exchange transactions (rate, amounts, timestamps).

## 9. Risks and open questions

- <e.g. processor converts at its own rate before we see the funds; who owns the spread?>
