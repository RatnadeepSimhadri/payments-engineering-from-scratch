---
name: checklist-numeric-boundaries
description: Review checklist for every place a number changes type or representation in Java, Python, or Go code that handles amounts, rates, or counts
phase: 0
lesson: 7
tags: [arithmetic, floating-point, overflow, rounding, review]
---

# Numeric Boundaries Checklist

A number is safe in the middle of a function and dangerous at its edges: where it is parsed, converted, multiplied by a rate, divided, stored, or sent. Walk each boundary in the change under review.

## Parsing (string to number)

- [ ] Amounts are parsed from decimal strings into integer minor units (`Fixed2.parse`, `Money.parse`), never via `Double.parseDouble` / `float()` / `strconv.ParseFloat` and a multiply.
- [ ] More decimals than the scale allows is rejected, not rounded ("19.995" is an error for a two-decimal type).
- [ ] Non-numeric input, thousands separators, and currency symbols are rejected rather than stripped silently.
- [ ] Tests include a value with fewer decimals than the scale ("7", "0.5") and confirm padding, not misplacement.

## Conversion from a float (when unavoidable)

- [ ] Any float that must become an amount goes through the shortest round-trip string (`BigDecimal.valueOf`, `repr`, `FormatFloat(v, 'f', -1, 64)`) and then the string parser. Never `(long)(v * 100)`, `int(v * 100)`, or `math.Round(v * 100)`.
- [ ] `NaN` and infinities are rejected before formatting.
- [ ] A test asserts that a float with more decimals than the scale (`0.1 + 0.2`) is rejected, so the binary error surfaces instead of becoming an off-by-one cent.

## Integer arithmetic

- [ ] Java: `Math.addExact`, `subtractExact`, `multiplyExact`, `negateExact` (or the lesson's `Checked`) on every `long` that holds an amount or a count that can grow. Plain `+` on amounts is a defect.
- [ ] Go: hand-written checked helpers returning `error` on every `int64` amount operation. `int64` wraps silently and the standard library offers no checked variant.
- [ ] Python: an explicit int64 range check wherever the value will reach a `BIGINT` column, a JSON consumer in another language, or a struct-packed wire format.
- [ ] Tests include `MAX + 1`, `MIN - 1`, `MIN * -1`, `-1 * MIN`, and `-MIN`.

## Multiplication and division

- [ ] Rates are exact rationals (`29/1000`) or decimal-typed, never `double`. `amount * 0.029` is a defect.
- [ ] Every operation that can produce a fraction names its rounding mode at the call site. No default mode hidden in a helper.
- [ ] `HALF_EVEN` unless a rule says otherwise; the rule is cited in a comment when it does.
- [ ] Division of an amount into parts uses a conserving allocation (Phase 10, Lesson 01), not repeated `dividedBy`. A test asserts the parts sum to the whole.
- [ ] Negative operands: truncation toward zero in all three languages. Python code does not use `//` or `%` on possibly-negative amounts without the magnitude-and-sign pattern.

## Accumulation

- [ ] Sums of amounts are integer sums. A `double` accumulator over amounts is a defect even if each term is "small".
- [ ] Any float sum that is genuinely needed (statistics, not money) uses compensated summation (`math.fsum`, or document that Python 3.12+ `sum()` compensates) and says so.
- [ ] Rounding happens once, at the end of a calculation, not after each step; or, if intermediate rounding is a business rule (per-line tax), the test covers the tie case.

## Storage and wire

- [ ] Columns are `BIGINT` (minor units) or `NUMERIC(p, s)` with explicit scale. Never `FLOAT`, `REAL`, `DOUBLE PRECISION`, or PostgreSQL `money`.
- [ ] JSON amounts are integers in minor units or decimal strings, never fractional JSON numbers.
- [ ] A value that could exceed 2^63 - 1 minor units (aggregate reports, fraud scores times volumes) is identified and either bounded or stored in a wider type on purpose.

## Language-specific traps to grep for

| Language | Grep for | Why |
|----------|----------|-----|
| Java | `new BigDecimal(` followed by a non-string | `new BigDecimal(0.1)` carries the binary error; use `valueOf` or a string |
| Java | `(long) (` or `(int) (` next to `* 100` | the truncation bug |
| Python | `Decimal(` with a float literal, `int(x * 100)`, `//` on amounts | same binary error; floor vs truncate |
| Python | `round(x, 2)` on money | `round` uses the float's binary value and banker's rounding on it, not on the decimal |
| Go | `float64` fields in amount structs, `FloatString(` for fees | no decimal type; `FloatString` rounds half away from zero without saying so |
| Go | `int64(x * 100)` | the truncation bug |
| All | a float literal with two decimals next to a currency code | an amount that is about to be a float |
