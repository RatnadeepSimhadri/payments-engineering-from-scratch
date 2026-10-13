---
name: checklist-money-handling
description: Pull-request review checklist for any change that creates, moves, stores, or displays an amount of money
phase: 10
lesson: 1
tags: [money, review, rounding, currency]
---

# Money Handling Review Checklist

Use on every pull request that touches an amount. One unchecked box is a reason to request changes.

## Representation

- [ ] Amounts are integers in the currency's minor unit (`long` / `int` / `int64`), never `double`, `float`, `float64`, or a decimal constructed from a float.
- [ ] Every amount is paired with its currency in the same type, struct, or row. No bare integers named `amount`.
- [ ] The currency's exponent comes from data (ISO 4217 table or a `Currency` registry), not from a hard-coded `/ 100`.
- [ ] Database columns are `BIGINT` + `CHAR(3)` or `NUMERIC` with an explicit scale. No `FLOAT`, `REAL`, `DOUBLE PRECISION`, or PostgreSQL `money`.

## Boundaries

- [ ] JSON, CSV, and message payloads carry amounts as integers in minor units or as decimal **strings**, never as JSON numbers with a fractional part.
- [ ] Parsing rejects more decimal places than the currency allows instead of rounding them away.
- [ ] Parsing rejects non-numeric input, thousands separators, and currency symbols rather than stripping them silently.
- [ ] Any conversion from a decimal library type back to minor units states its rounding mode in the code.

## Arithmetic

- [ ] Addition and subtraction use checked arithmetic (`Math.addExact`, hand-written `addChecked`) or an unbounded integer. Overflow is an error, never a wrap.
- [ ] Multiplication by a rate (fee %, tax %, FX) is done as an exact rational (`numerator / denominator`) or a decimal type, and the rounding mode is a named argument at the call site.
- [ ] The rounding mode matches the policy: `HALF_EVEN` for fees, taxes, and FX by default; `DOWN` for amounts owed by us; `UP` only where a fee must cover a cost. Any other choice has a comment citing the rule that requires it.
- [ ] Negative amounts round the same way as positives relative to zero (truncation, not floor). If Python is involved, `//` is not used on possibly-negative amounts.
- [ ] Splitting an amount uses a conservation-preserving allocation (largest remainder); the sum of the parts is asserted equal to the whole in a test.

## Cross-currency

- [ ] Adding, subtracting, or comparing two amounts of different currencies is a compile-time or runtime error with a specific exception type, never a silent coercion.
- [ ] FX conversion records the rate, its source, its timestamp, and the rounding mode used, alongside the converted amount.

## Display

- [ ] Locale formatting (symbols, separators) happens only at the presentation layer; internal logs and APIs use the canonical `"19.99 USD"` form.
- [ ] Zero-decimal (JPY) and three-decimal (KWD, BHD) currencies have at least one test case each.

## Tests present

- [ ] A rounding tie test for each mode used, including a negative tie.
- [ ] An allocation test where naive independent rounding would lose or invent a unit.
- [ ] A currency-mismatch test.
- [ ] An overflow test (or, in Python, a test that a value above 2^63 survives).
