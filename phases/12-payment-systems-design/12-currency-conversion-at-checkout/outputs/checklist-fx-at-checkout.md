---
name: checklist-fx-at-checkout
description: Review checklist for any change that prices in one currency and settles in another, including DCC
phase: 12
lesson: 12
tags: [fx, currency-conversion, dcc, rate-lock, rounding]
---

# FX at Checkout Review Checklist

Use on every pull request that displays a converted price, locks or applies an exchange rate, settles across currencies, or offers dynamic currency conversion. One unchecked box is a reason to request changes.

## Representation

- [ ] Rates are stored and computed as exact values (rational, or decimal strings parsed exactly), never as `float` or `double`.
- [ ] Every rate is directional (`from`, `to`), and conversion rejects an amount in the wrong source currency.
- [ ] Inverse rates are computed exactly from the quoted direction, not looked up from a separately rounded table.
- [ ] Conversion applies `10^(target exponent - source exponent)` from ISO 4217, with a test for a zero-exponent (JPY) and a three-exponent (KWD) currency.

## Rounding

- [ ] There is exactly one rounding per conversion, on the final minor-unit amount. No code path rounds the rate first.
- [ ] Every rounding call names its mode, and the policy (who absorbs the sub-unit) is written in the design doc.
- [ ] Splits of converted amounts (margin shares, partial refunds) use largest-remainder allocation and sum exactly.

## The lock

- [ ] The instant the rate is fixed is explicit, and so is the party that owns movement after it.
- [ ] The lock window is half-open, `[issuedAt, expiresAt)`, with tests at `expiresAt - 1` and `expiresAt`.
- [ ] An expired quote is rejected; checkout re-quotes and shows the new price before charging. No path silently uses a newer rate.
- [ ] The quote id, rate, mid, spread, source, and timestamp are stored with the order and included in the payment's idempotency fingerprint.

## Risk and settlement

- [ ] The merchant's settlement amount under a lock is the promised amount, copied, not recomputed at settlement.
- [ ] Settlement records the market rate used and the resulting FX P&L per order, in the settlement currency.
- [ ] Open-quote exposure per currency pair is reported to whoever hedges it.

## Refunds

- [ ] The refund rate policy (original rate or current rate) is chosen, disclosed, and recorded on each refund.
- [ ] Partial refunds can never exceed the presentment amount in total.

## DCC

- [ ] DCC is offered as an explicit choice and never preselected.
- [ ] The disclosure shows both amounts, the DCC rate, and the markup over the ECB (or other required) reference rate, computed rate-over-rate and exact.
- [ ] The reference rate's source and publication date are recorded with the transaction.
- [ ] The cardholder's choice is recorded, and the receipt matches the disclosure byte for byte.
