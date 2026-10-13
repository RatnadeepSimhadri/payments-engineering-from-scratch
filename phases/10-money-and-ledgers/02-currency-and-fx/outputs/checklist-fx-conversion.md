---
name: checklist-fx-conversion
description: Pull-request review checklist for any code that creates, stores, or applies an exchange rate or converts money between currencies
phase: 10
lesson: 2
tags: [fx, currency, iso-4217, rounding, review]
---

# FX Conversion Review Checklist

Use on every pull request that adds a currency, changes a rate provider, converts an amount, or opens a multi-currency balance product. One unchecked box is a reason to request changes. Pair it with `checklist-money-handling` from Lesson 01.

## Currency registry

- [ ] Every currency comes from one registry loaded from ISO 4217 data (or `java.util.Currency` wrapped), never from a string literal plus a hard-coded `/ 100`.
- [ ] The registry is an allow-list: a code the business does not support is rejected at the parsing boundary with a specific exception, before it reaches the ledger.
- [ ] Both alphabetic and numeric codes are registered and unique; anything that speaks ISO 8583 or network settlement files maps by numeric code.
- [ ] Zero-decimal (JPY, KRW), three-decimal (KWD, BHD), and four-decimal (CLF) currencies each have at least one test if the product could ever see them.
- [ ] Pseudo-currencies (`XAU`, `XXX`) and JDK `-1` fraction digits are handled explicitly or rejected.
- [ ] There is a documented process for applying an ISO 4217 amendment (new code, retired code, changed exponent) without a code change.

## Rate representation

- [ ] Rates are parsed from strings into an exact rational (numerator/denominator or a decimal type), never through `double`, `float`, or a JSON number with a fraction.
- [ ] Each stored rate carries: base, quote, value, source, observation timestamp, and whether it is mid, bid, or ask (or the spread in bps).
- [ ] The direction is unambiguous in the type and the field names: `USD/EUR = 0.9217` means 1 USD buys 0.9217 EUR. No field is called just `rate`.
- [ ] Inverting a rate swaps numerator and denominator exactly; nothing stores a pre-rounded `1 / rate`.
- [ ] Bid and ask are derived from mid with integer arithmetic, and a rate with its spread already applied cannot have it applied a second time.
- [ ] Cross rates are derived by exact division through one base currency, inherit the older timestamp and wider spread, and are never rounded before use.

## Freshness and provenance

- [ ] Every rate lookup checks age against a written maximum using an injected clock, and fails closed with a specific exception (`StaleRateException`) rather than using the last known value silently.
- [ ] The maximum age is configured per use case: display, customer quote, settlement. Settlement rates are the ones the counterparty actually used, not whatever was cached.
- [ ] A rate table quotes against one base currency and refuses rates in any other base.
- [ ] Manual overrides (treasury) are recorded with a source like `treasury-manual-override`, an author, and an expiry.

## Conversion

- [ ] Conversion is one exact multiplication including both currencies' exponents, followed by exactly one rounding into the target minor unit.
- [ ] The rounding mode is a named argument at the call site and matches written policy (`HALF_EVEN` by default; `DOWN` when we owe the customer and the policy says so).
- [ ] The amount's currency is checked against the rate's base; converting EUR with a USD/EUR rate is a `CurrencyMismatchException`, not a silent inversion.
- [ ] Intermediate products use checked arithmetic (`multiplyExact`) or an unbounded integer; overflow is an error, never a wrap.
- [ ] The conversion result records: input amount, output amount, rate value and provenance, rounding mode, and the discarded remainder.
- [ ] Batches state which total is authoritative: sum of converted items, or conversion of the summed input. The difference is computed and posted, not ignored.

## Residue and accounting

- [ ] Rounding residue (including round-trip residue) is computed, signed, and posted to a dedicated FX rounding gain/loss account.
- [ ] Spread revenue is posted separately from rounding residue so finance can tell pricing income from arithmetic dust.
- [ ] Multi-currency movements go through an FX clearing account per currency so each currency's entries balance on their own.
- [ ] Reconciliation recomputes conversions from the stored rate and rounding mode and matches to the minor unit.

## Tests present

- [ ] A conversion between currencies of different exponents (2 to 0, 2 to 3).
- [ ] A round trip with a positive residue and one with a negative residue.
- [ ] An exact rate (such as `1/2`) with zero residue, to prove the code does not invent error.
- [ ] A stale rate rejected, an unknown currency rejected, and a currency-mismatch rejected.
- [ ] A cross rate compared against an independently computed exact value.
- [ ] An overflow case for very large amounts times large rate numerators.
