# Decimal and Integer Arithmetic in Three Runtimes

> 0.1 + 0.2 is why every ledger stores integers.

**Type:** Build
**Languages:** Java, Python, Go
**Prerequisites:** Phase 0, Lesson 03 (Testing from the Standard Library)
**Time:** ~60 minutes

## Learning Objectives

- Explain, from the binary64 format, why `0.1 + 0.2 != 0.3` and why `(long)(19.99 * 100)` is 1998, and reproduce both in each runtime
- Measure accumulation error over a million additions and distinguish it from rounding bias, which is a property of the rounding rule rather than the number format
- State what each runtime does on 64-bit integer overflow (Java and Go wrap, Python grows) and implement checked `add`, `sub`, `mul`, and `negate` that refuse to wrap
- Build a `Fixed2` fixed-point type with exactly two decimals, explicit rounding on every inexact operation, and a safe conversion from `double` through the shortest round-trip string
- Compare `Fixed2` with `java.math.BigDecimal`, Python's `decimal.Decimal`, and Go's `math/big.Rat`, and say which trap each one has
- Explain why `Fixed2` is not yet a money type and what Phase 10, Lesson 01 adds to it

## The Problem

A settlement service written in Java computes each merchant's daily payout as `sum(sales) * (1 - feeRate)` with `double` arithmetic and then converts to cents with `(long)(payout * 100)`. A sister service, written in Python, computes the same figure for the merchant dashboard. For a merchant with a single sale of 19.99 and a 0% fee the Java service pays 1998 cents, because `19.99 * 100` in binary64 is `1998.9999999999998` and the cast truncates. The Python dashboard shows 19.99 because Python's `repr` prints the shortest decimal that round-trips, which hides the same binary value behind a friendlier string. The merchant sees 19.99, receives 19.98, and opens a support ticket. Multiply by eight million payouts a month and the support queue has a name for it: penny skimming. Nobody skimmed anything.

A second team stores balances as `long` cents in a Go service and never touches a float. Their failure is different. A test fixture creates a stress account with a balance near `math.MaxInt64` to check formatting of very large numbers. A credit of one cent arrives. Go's `+` on `int64` wraps silently, the balance becomes `math.MinInt64`, and the account is now 92 quadrillion dollars overdrawn. The overdraft job fires. The alerting is loud, the fix is one line, and the post-mortem asks why the language did not complain. It does not complain because the Go specification says integer overflow wraps, and the Java Language Specification says the same thing about `long`. Python would have kept counting, into a 64-bit-plus integer that no database column can store, and failed later, somewhere else.

Both failures are about which number system you are in. Binary floating point is a fixed number of significant bits with a floating binary point; it represents `0.25` exactly and `0.1` not at all. Fixed-width integers are exact until they are not, and then they are silently wrong. Arbitrary-precision integers are exact forever and do not fit in your database. This lesson shows each failure on purpose in all three runtimes, builds checked integer arithmetic that turns silent wrap into a loud error, and builds `Fixed2`, a two-decimal fixed-point type where every rounding decision is yours and spelled out. It is the type Phase 10, Lesson 01 grows into `Money` by adding a currency and a conserving allocation.

## The Concept

### Three number systems

| | Binary floating point (`double`, `float64`, `float`) | Fixed-width integer (`long`, `int64`) | Arbitrary-precision integer (Python `int`, `BigInteger`, `big.Int`) | Decimal (`BigDecimal`, `Decimal`) |
|---|---|---|---|---|
| Represents exactly | sums of powers of two with 53 significant bits: `0.5`, `0.25`, `19.75` | every integer in [-2^63, 2^63-1] | every integer | every decimal fraction of bounded digits |
| `0.1` | nearest value is `0.1000000000000000055511...` | not representable (store `1` tenth instead) | not representable (store `1` tenth instead) | exact |
| `19.99 * 100` | `1998.9999999999998` | `1999 * 100 = 199900` exact | exact | exact |
| On exceeding range | rounds to a neighbour; eventually `Infinity` | wraps silently (Java, Go) | grows | grows (or throws on a scale limit) |
| Associative addition | no: `(a + b) + c != a + (b + c)` in general | yes, within range | yes | yes |
| Speed | one CPU instruction | one CPU instruction | tens to hundreds of instructions | hundreds of instructions, allocation |
| Right for money? | never | yes, as a count of minor units, with checked arithmetic | yes in Python, with an explicit range check at the storage boundary | at boundaries (parsing, rate math, display) |

The row that matters most is `19.99 * 100`. A `double` cannot hold `19.99`; the nearest binary64 value is `19.989999999999998436805981327779591083526611328125`. Multiply by 100, round to the nearest double, and you land on `1998.9999999999998`, which truncates to 1998. Not every price fails this way (`10.10 * 100` happens to round to exactly `1010.0`), which is what makes the bug hard to catch with a handful of test values: it depends on which side of the nearest double each product falls.

### Why `0.1 + 0.2` is not `0.3`

A binary64 value is `sign * 1.f * 2^e` with 52 explicit fraction bits. `0.1` in binary is `0.0001100110011...` repeating, because 10 has a factor of 5 and 5 does not divide any power of 2. The stored value is the nearest 53-bit truncation, slightly above `0.1`. `0.2` is the same pattern shifted one bit, also slightly above. Their sum, rounded again to 53 bits, is `0.3000000000000000444089209850062616169452667236328125`. The nearest double to `0.3` is `0.299999999999999988897769753748434595763683319091796875`. These are different doubles, one unit in the last place apart, so `==` says false and the shortest round-trip string says `0.30000000000000004`.

Every runtime prints the same digits because every runtime uses the same hardware format and the same shortest-round-trip algorithm for `Double.toString`, `repr`, and `strconv.FormatFloat(v, 'g', -1, 64)`. One trap is language-specific: Go evaluates constant expressions exactly at compile time, so `fmt.Println(0.1 + 0.2)` prints `0.3`. The demo stores the operands in variables first; that is the difference between testing the compiler's arbitrary-precision constant folding and testing the CPU.

### Accumulation error versus rounding bias

These are two different phenomena and the lesson keeps them apart.

**Accumulation error** is a property of the float format. Add `0.01` to an accumulator a million times and each addition rounds to 53 bits; the result is `10000.0000001719`, high by `1.7e-7`. Add `0.1` ten times and you get `0.9999999999999999`. The error is small per step, random-looking in sign, and grows roughly with the square root of the step count when the inputs vary, or linearly when they do not (every `0.01` rounds the same way). The fix is to change number system: a million additions of `Fixed2("0.01")` is `10000.00` exactly, because integer addition does not round.

Python since 3.12 has a trap here: the built-in `sum()` of floats uses Neumaier compensated summation, so `sum([0.1] * 10) == 1.0`. `math.fsum` has always done this. An explicit `for` loop with `acc += term` shows the raw error, which is why `sumRepeated` is a loop in all three languages. A Python engineer who checks the claim with `sum()` will conclude that floats are fine, and will be wrong about every other operation.

**Rounding bias** is a property of the rounding rule and appears even with exact arithmetic. Take the hundred values `0.5, 1.5, 2.5, ..., 99.5`, whose exact sum is `5000`, and round each to an integer:

```text
HALF_UP   : 1 + 2 + 3 + ... + 100 = 5050    drift +50   (every tie goes up)
HALF_EVEN : 0 + 2 + 2 + 4 + 4 + ... + 100 = 5000   drift 0   (ties alternate down, up)
DOWN      : 0 + 1 + 2 + ... + 99 = 4950     drift -50
UP        : same as HALF_UP here            drift +50
```

The demo computes exactly this with integer arithmetic on tenths (`5, 15, ..., 995` divided by 10), so there is no float anywhere and the drift is purely the rule's. Fee calculations on round prices produce exact half-cent ties constantly (2.5% of 1.00 is 2.5 cents). A system that rounds `HALF_UP` drifts upward by half a cent per tie, forever, in one direction; a system that rounds `HALF_EVEN` has zero expected drift. That is why `HALF_EVEN` is the default in Phase 10 and in every tax authority's rounding guidance that specifies a rule.

### Integer overflow: wrap, grow, or refuse

A `long` or `int64` holds 2^64 values. The hardware adds modulo 2^64 and interprets the result in two's complement, so `9223372036854775807 + 1` is `-9223372036854775808`. The Java Language Specification (15.18.2) and the Go specification ("Integer overflow") both define this as the language's behaviour, not as an error. Python's `int` has no fixed width; `2**63` is just a bigger integer. None of the three raises by default.

The checked versions detect the wrap after the fact with bit tricks that need no wider type:

```text
add:  r = a + b.  Overflowed iff a and b have the same sign and r has the other one.
      ((a ^ r) & (b ^ r)) < 0   is true exactly then (the sign bit survives both XORs only on overflow).
sub:  r = a - b.  Overflowed iff a and b have different signs and r's sign differs from a's.
      ((a ^ b) & (a ^ r)) < 0
mul:  r = a * b.  If a != 0 and r / a != b, the product wrapped. One case escapes the division test:
      -1 * MIN_VALUE wraps to MIN_VALUE, and MIN_VALUE / -1 also wraps to MIN_VALUE, so check it explicitly.
neg:  -MIN_VALUE is MIN_VALUE. The only input that overflows.
```

Trace `add(Long.MAX_VALUE, 1)`: `a = 0x7FFF...F`, `b = 1`, `r = 0x8000...0`. `a ^ r = 0xFFFF...F` (negative), `b ^ r = 0x8000...1` (negative), AND of two negatives is negative, so overflow. Trace `add(-1, 1)`: `r = 0`, `a ^ r = -1`, `b ^ r = 1`, AND is `1`, positive, no overflow. Java has `Math.addExact` and friends that do exactly this; Go has nothing in the standard library, so the Go lesson writes the same four functions by hand, and the Java lesson writes them too so the two files are the same code and the tests can confirm they agree with `Math.addExact`.

Python's `checked_add` is a range check: compute the exact result, raise `OverflowError` if it lies outside [-2^63, 2^63-1]. Why bother, when Python would not have wrapped? Because the Python service is one hop from a `BIGINT` column, a Java consumer, or a Go consumer, each of which has the limit. The place to find out is at the arithmetic, with a stack trace that names the operands, not when a JDBC driver rejects the insert.

### `Fixed2`: a fixed-point type where every rounding is yours

`Fixed2` stores a `long` count of hundredths. `19.99` is `1999`. Addition and subtraction are checked integer operations and never round. Three operations can produce a fraction, and each takes a `Rounding`:

- `times(Fixed2, Rounding)`: the product of two two-decimal numbers has four decimals (`1999 * 108 = 215892`, which is `21.5892`), so two must go. `divideRounded(215892, 100, HALF_EVEN) = 2159`, that is `21.59`.
- `timesRate(numerator, denominator, Rounding)`: a rate is an exact rational, never a double. 2.9% is `29/1000`; `1999 * 29 = 57971`, `/ 1000 = 57 r 971`, more than half, so `58` cents under every mode except `DOWN`.
- `dividedBy(long, Rounding)`: `100.00 / 3 = 3333 r 1`, `HALF_EVEN` gives `33.33`.

```mermaid
flowchart LR
  S["\"19.99\" (string)"] -->|parse: reject >2 decimals| F["Fixed2 hundredths=1999"]
  D["19.99 (double)"] -->|fromDouble: shortest string \"19.99\" then parse| F
  D -. "(long)(v * 100)" .-> X1["1998: wrong"]
  F -->|plus / minus: checked, exact| F
  F -->|"times(Fixed2, Rounding)<br/>timesRate(n, d, Rounding)<br/>dividedBy(n, Rounding)"| F
  F -. "\"19.995\"" .-> X2["ArithmeticException: more than two decimals"]
  F -. "MAX + 0.01" .-> X3["ArithmeticException: long overflow"]
  F -->|Phase 10 L01 adds Currency + allocate| M["Money"]
```

`fromDouble` is the bridge from a float, and it is the one place the lesson lets a double in. It does not multiply by 100. It asks the runtime for the shortest decimal string that round-trips to the same double (`BigDecimal.valueOf(v).toPlainString()`, `repr(v)`, `strconv.FormatFloat(v, 'f', -1, 64)`), which is `"19.99"` for the double nearest 19.99, and then parses that string with the same two-decimal rule as any other input. `fromDouble(0.1 + 0.2)` is therefore rejected with "more than two decimals", which is the correct outcome: the binary error is surfaced at the boundary instead of being truncated into a cent that is off by one.

### What `Fixed2` does not do

It has no currency, so it will happily add 19.99 of dollars to 19.99 of yen; the result is meaningless and nothing stops you. It has a fixed scale of two, so it cannot hold yen (zero decimals) correctly or Kuwaiti dinar (three) at all. It has no allocation, so `100.00 / 3` gives `33.33` and `33.33 * 3` gives `99.99`; one cent has vanished, and the type has no operation that conserves it. Phase 10, Lesson 01 fixes all three: `Money` pairs the integer with an ISO 4217 `Currency` whose exponent sets the scale, refuses to combine currencies, and provides `allocate` with the largest-remainder method so parts always sum to the whole. `Fixed2` is the arithmetic core of that type with the currency stripped away so the number-system lesson is not tangled up with the currency lesson.

## Build It

The walkthrough is in Java. Python and Go subsections show only what differs.

### Step 1: Make the float failures reproducible

```java
final class Floats {
    static double add(double a, double b) { return a + b; }

    static long naiveCents(double amount) { return (long) (amount * 100); }

    static double sumRepeated(double term, int times) {
        double acc = 0.0;
        for (int i = 0; i < times; i++) acc += term;
        return acc;
    }
}
```

These exist so the tests can pin the failures: `add(0.1, 0.2) != 0.3`, `naiveCents(19.99) == 1998`, `sumRepeated(0.1, 10) != 1.0`, and the control case `sumRepeated(0.25, 4) == 1.0` because `0.25` is exact. A test that asserts a bug exists is how you stop a future refactor from "fixing" the demo into something that no longer demonstrates anything.

**Predict:** `naiveCents(10.10)`. Is it 1009 or 1010? Compute `10.10 * 100` in any REPL before you decide, and then say why the lesson chose 19.99.

### Step 2: Checked arithmetic by hand

```java
final class Checked {
    static long add(long a, long b) {
        long r = a + b;
        if (((a ^ r) & (b ^ r)) < 0) throw new ArithmeticException("long overflow: " + a + " + " + b);
        return r;
    }

    static long sub(long a, long b) {
        long r = a - b;
        if (((a ^ b) & (a ^ r)) < 0) throw new ArithmeticException("long overflow: " + a + " - " + b);
        return r;
    }

    static long mul(long a, long b) {
        long r = a * b;
        if (a != 0 && (r / a != b || (a == -1 && b == Long.MIN_VALUE))) {
            throw new ArithmeticException("long overflow: " + a + " * " + b);
        }
        return r;
    }

    static long negate(long a) {
        if (a == Long.MIN_VALUE) throw new ArithmeticException("long overflow: -(" + a + ")");
        return -a;
    }

    static long wrappingAdd(long a, long b) { return a + b; }
}
```

The JDK has `Math.addExact`, and production Java should use it. Writing the checks out here does two things: it shows what `addExact` does (the JIT compiles it to the same overflow-flag check), and it gives Go, which has no equivalent, the identical code. `wrappingAdd` is plain `+` with a name that admits what it does, so the demo can print the wrapped value next to the exception.

**Predict:** `Checked.mul(Long.MIN_VALUE, -1)`. Which branch catches it, the division test or the explicit case? Now `Checked.mul(-1, Long.MIN_VALUE)`.

### Step 3: Division that rounds the way you say

```java
static long divideRounded(long dividend, long divisor, Rounding rounding) {
    if (divisor == 0) throw new ArithmeticException("division by zero");
    long quotient = dividend / divisor;        // truncates toward zero
    long remainder = dividend % divisor;       // same sign as dividend
    if (remainder == 0) return quotient;
    int sign = (dividend < 0) ^ (divisor < 0) ? -1 : 1;
    long twiceRemainder = Math.abs(remainder) * 2;
    long absDivisor = Math.abs(divisor);
    return switch (rounding) {
        case DOWN -> quotient;
        case UP -> quotient + sign;
        case HALF_UP -> twiceRemainder >= absDivisor ? quotient + sign : quotient;
        case HALF_EVEN -> {
            if (twiceRemainder > absDivisor) yield quotient + sign;
            if (twiceRemainder < absDivisor) yield quotient;
            yield (quotient % 2 == 0) ? quotient : quotient + sign;
        }
    };
}

static long roundingBias(Rounding rounding) {
    long sum = 0;
    for (long tenths = 5; tenths < 1000; tenths += 10) sum += divideRounded(tenths, 10, rounding);
    return sum - 5000;
}
```

This is the same function as Phase 10, Lesson 01, reproduced so this lesson is self-contained. Comparing `2 * remainder` with the divisor decides "more than half, less than half, exactly half" without ever forming a fraction. `roundingBias` is the bias experiment from The Concept as code: a hundred exact ties, one rounding rule, the drift from the exact sum.

**Predict:** `roundingBias(HALF_EVEN)` is 0 for the values `0.5 ... 99.5`. Would it still be 0 for `0.5 ... 98.5` (99 values)? What is the smallest nonzero drift `HALF_EVEN` can show on a set of exact ties?

### Step 4: `Fixed2`

```java
record Fixed2(long hundredths) implements Comparable<Fixed2> {
    static Fixed2 parse(String text) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        if (s.isEmpty()) throw new NumberFormatException("empty amount");
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) {
            throw new NumberFormatException("not a decimal: " + text);
        }
        if (frac.length() > 2) throw new ArithmeticException(text + " has more than two decimals");
        while (frac.length() < 2) frac += "0";
        long value = Checked.add(Checked.mul(Long.parseLong(whole), 100), Long.parseLong(frac));
        return new Fixed2(negative ? -value : value);
    }

    static Fixed2 fromDouble(double value) {
        if (Double.isNaN(value) || Double.isInfinite(value)) throw new ArithmeticException("not finite: " + value);
        return parse(BigDecimal.valueOf(value).toPlainString());
    }
}
```

The parser never touches a `double`: it splits at the dot, validates digits, pads `"5"` to `"50"`, and rejects a third decimal. `fromDouble` goes through `BigDecimal.valueOf`, which is documented to use `Double.toString`, the shortest round-trip representation; `toPlainString` avoids `1E+2` for large values. The result is then held to the same two-decimal rule as any string.

```java
    Fixed2 times(Fixed2 other, Rounding rounding) {
        long product = Checked.mul(hundredths, other.hundredths);          // scaled by 10^4
        return new Fixed2(Checked.divideRounded(product, 100, rounding));
    }

    Fixed2 timesRate(long numerator, long denominator, Rounding rounding) {
        if (denominator <= 0) throw new ArithmeticException("denominator must be positive");
        return new Fixed2(Checked.divideRounded(Checked.mul(hundredths, numerator), denominator, rounding));
    }

    Fixed2 dividedBy(long divisor, Rounding rounding) {
        return new Fixed2(Checked.divideRounded(hundredths, divisor, rounding));
    }
```

Every method that can produce a fraction has `Rounding` in its signature; there is no overload that picks a default, so a reviewer sees the choice at every call site. `plus`, `minus`, and `negate` delegate to `Checked` and are not shown.

**Predict:** `Fixed2.parse("0.05").times(Fixed2.parse("0.05"), HALF_EVEN)`. The exact product is `0.0025`. What does `HALF_EVEN` return, and what does `UP` return? Which one would a fee schedule that says "minimum one cent" want?

### Step 5: Run it

```bash
cd phases/00-setup-and-tooling/07-decimal-and-integer-arithmetic/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

```console
== 0.1 + 0.2 ==
0.1 + 0.2 = 0.30000000000000004  (equals 0.3? false)
(long)(19.99 * 100) = 1998  <- the penny-skimming bug (19.99 * 100 = 1998.9999999999998)
Fixed2.fromDouble(19.99) = 19.99  (via the shortest decimal string "19.99")

== Accumulation ==
0.1 added 10 times as double = 0.9999999999999999
0.01 added 1,000,000 times as double = 10000.0000001719 (error 1.719e-07)
0.01 added 1,000,000 times as Fixed2 = 10000.00 exactly

== Rounding bias on 100 exact half-ties (0.5, 1.5, ..., 99.5) ==
HALF_EVEN drift = +0
HALF_UP   drift = +50
DOWN      drift = -50
UP        drift = +50

== Overflow ==
Long.MAX_VALUE + 1 with plain + = -9223372036854775808  (wrapped to Long.MIN_VALUE)
Checked.add(Long.MAX_VALUE, 1) -> ArithmeticException: long overflow: 9223372036854775807 + 1

== Fixed2: two decimals, explicit rounding ==
19.99 x 3        = 59.97
19.99 x 2.9%     = 0.58  (57.971 hundredths -> 58)
19.99 x 1.08     = 21.59  (2158.92 hundredths -> 2159)
100.00 / 3       = 33.33, and 33.33 x 3 = 99.99  <- why Phase 10 adds allocate()
rejected: 19.995 has more than two decimals

== Decimal types in the standard library ==
new BigDecimal("0.1").add(new BigDecimal("0.2")) = 0.3
new BigDecimal(0.1)  (from a double)             = 0.1000000000000000055511151231257827021181583404541015625
```

The Python and Go programs print the same sections with the same numbers, down to `10000.0000001719`, because the float hardware and the shortest-repr algorithm are the same. Only the overflow section differs, and that difference is the lesson.

### In Python

```python
def checked_add(a: int, b: int) -> int:
    return _require_int64(a + b, f"{a} + {b}")      # Python never wraps; the check is against the int64 range

def wrapping_add(a: int, b: int) -> int:
    u = (a + b) & _MASK64                            # emulate what Java and Go do, for the demo
    return u - (1 << 64) if u > INT64_MAX else u

def sum_repeated(term: float, times: int) -> float:
    acc = 0.0
    for _ in range(times):                           # not sum(): since 3.12 sum() compensates and hides the error
        acc += term
    return acc
```

Differences, all in `code/python/main.py`:

- **No overflow to detect, so the check is a range check.** `checked_add` computes the exact result and raises `OverflowError` if it would not fit in an `int64`. The test `INT64_MAX + 1 == 2**63` records the language's behaviour; the test that `checked_add(INT64_MAX, 1)` raises records the design's.
- **`wrapping_add` is an emulation**, masking to 64 bits and reinterpreting the sign, so the three demos can print the same wrapped number side by side.
- **`sum()` compensates since Python 3.12.** `sum([0.1] * 10)` is `1.0` and `math.fsum` always was. The lesson's loop deliberately does not use either. There is a test (`test_python_sum_hides_the_error_since_3_12`) that asserts `math.fsum([0.1] * 10) == 1.0`, so a reader who wonders why the loop exists finds the answer in the suite.
- **`//` floors; Java and Go truncate.** `-129 // 10` is `-13` in Python, `-12` in the others. `divide_rounded` works on magnitudes and reapplies the sign so the three agree on every negative input; the test asserts both the function's answer and the raw `//` answer so the difference is on record.
- **`Fixed2` rejects floats at construction** (`Fixed2(19.99)` raises `TypeError`, with `bool` excluded because `True` is an `int`), and `from_double` goes through `repr`, which is the shortest round-trip string, then `Decimal` for a plain (non-exponent) format, then `parse`.

### In Go

```go
func CheckedMul(a, b int64) (int64, error) {
    r := a * b
    if a != 0 && (r/a != b || (a == -1 && b == math.MinInt64)) {
        return 0, fmt.Errorf("%w: %d * %d", ErrOverflow, a, b)
    }
    return r, nil
}

func FromDouble(value float64) (Fixed2, error) {
    if math.IsNaN(value) || math.IsInf(value, 0) {
        return Fixed2{}, fmt.Errorf("not finite: %v", value)
    }
    return Parse(strconv.FormatFloat(value, 'f', -1, 64))   // precision -1: shortest round-trip
}
```

Differences, all in `code/go/main.go`:

- **Errors are values.** Every checked operation returns `(int64, error)`, `Fixed2` methods return `(Fixed2, error)`, and the sentinels `ErrOverflow` and `ErrTooManyDecimals` support `errors.Is`. The demo uses a small `must` helper to keep the output readable; production code would not.
- **No `Math.multiplyExact` to compare against**, so the hand-written checks are the only checks; the Java test that compares `Checked` with `Math.*Exact` has no Go counterpart.
- **Constant folding.** `0.1 + 0.2` as a constant expression is evaluated exactly by the compiler and prints `0.3`. `AddFloats(a, b)` with variables is the run-time addition. The test stores the operands in variables for the same reason.
- **No decimal type in the standard library.** The closest thing is `math/big.Rat`, an exact rational: `Rat("0.1") + Rat("0.2")` is `3/10`, and `new(big.Rat).SetFloat64(0.1)` is `3602879701896397/36028797018963968`, which is the binary value written as a fraction. Go payment services use `int64` minor units and bring in a decimal library only at the JSON boundary.

## Use It

### `java.math.BigDecimal`

An arbitrary-precision decimal with a `scale` and eight `RoundingMode`s. `new BigDecimal("19.99").multiply(new BigDecimal("0.029")).setScale(2, RoundingMode.HALF_EVEN)` is `0.58`. What it does that `Fixed2` made explicit: it carries the scale with the value, so the product of two two-decimal numbers is a four-decimal number until you `setScale`, and it cannot overflow. What it hides: `new BigDecimal(0.1)` from a `double` is exact in the wrong way, carrying all 55 digits of the binary value, where `new BigDecimal("0.1")` and `BigDecimal.valueOf(0.1)` give `0.1`. The trap is the constructor overload. `Fixed2.fromDouble` goes through `BigDecimal.valueOf` for exactly this reason.

### Python `decimal` and `fractions`

`Decimal("0.1") + Decimal("0.2") == Decimal("0.3")`. The module has a context with a precision (28 digits by default) and nine rounding modes; `quantize(Decimal("0.01"), rounding=ROUND_HALF_EVEN)` is the `setScale` equivalent. The same trap: `Decimal(0.1)` is `0.1000000000000000055511151231257827021181583404541015625`. `fractions.Fraction` is the exact rational and is what `timesRate(29, 1000, ...)` is doing by hand. Neither has an int64 limit, which is why the Python `Fixed2` keeps one.

### Go `math/big`

`big.Int` for arbitrary integers and `big.Rat` for exact rationals, no decimal. `big.Rat.FloatString(2)` formats with a fixed number of decimals and rounds half away from zero, which is `HALF_UP` and not `HALF_EVEN`; a service that formats fees with `FloatString` has chosen a rounding mode without writing its name down. Go's `strconv.FormatFloat(v, 'f', -1, 64)` is the shortest-round-trip formatter and is the same algorithm as Java's `Double.toString` and Python's `repr`.

### Databases and wire formats

`BIGINT` for minor units; `NUMERIC(19, 4)` with an explicit rounding rule on read if a decimal column is unavoidable; never `FLOAT`, `REAL`, or `DOUBLE PRECISION` for an amount. In JSON, send amounts as integers in minor units (`"amount": 1999`, the Stripe convention) or as strings (`"amount": "19.99"`), never as a JSON number with a fraction, because the first parser that reads it into a `double` recreates `1998.9999999999998`. Checked arithmetic belongs in the application; `BIGINT` columns raise on overflow in PostgreSQL but a value computed in the application and wrapped before the insert arrives as a plausible negative number.

### IEEE 754 decimal

The 2008 revision of IEEE 754 added `decimal64` and `decimal128`, binary-coded decimal formats with a decimal exponent. IBM's POWER and z hardware implement them; mainstream x86 and ARM do not, and none of the three languages exposes them natively. `BigDecimal` and `Decimal` implement the same arithmetic in software, which is where the hundred-fold speed difference against `long` comes from, and why the hot path of a ledger is integer arithmetic with decimal types only at the edges.

## Ship It

`outputs/checklist-numeric-boundaries.md` is a review checklist for the places a number changes type: parsing from a string, converting from a float, arithmetic that can overflow, division that can round, and writing to a column or a wire format. It lists the question to ask at each boundary and the test that proves the answer. Use it alongside Phase 10, Lesson 01's money-handling checklist, which assumes these boundaries are already right.

## Exercises

1. **Reinforce.** Add `Rounding.HALF_DOWN` (ties toward zero) to `divideRounded` and `roundingBias` in all three languages. Predict the drift over the hundred ties before you run it, then run it. Explain in one sentence why the drift of `HALF_DOWN` equals that of `DOWN` on this input but would differ on `0.6, 1.6, ..., 99.6`.
2. **Apply.** Write `Floats.worstPrices(limit)` that scans every two-decimal price from `0.01` to `limit` and returns the ones for which `(long)(price * 100)` is off by one. Report how many of the 9999 prices below `100.00` fail, and whether `Math.round(price * 100)` fixes all of them (it does not fix `x.xx5` inputs, but there are none here; say why rounding is still the wrong fix in general and `fromDouble` is right).
3. **Extend.** Generalise `Fixed2` to `FixedN(scale)` where the scale is a field, so the same type can hold yen (scale 0) and dinar (scale 3), and make `times` of two values with different scales produce a value with the sum of the scales before rounding to the left operand's scale. Then read Phase 10, Lesson 01 and write a paragraph on why that lesson puts the scale on the `Currency` rather than on the value, and what bug a per-value scale would allow between two USD amounts.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| binary64 | "a double" | IEEE 754's 64-bit format: 1 sign bit, 11 exponent bits, 52 fraction bits; exact for sums of powers of two, approximate for `0.1` |
| Shortest round-trip | "`repr`" | The fewest decimal digits that parse back to the same double; what `Double.toString`, Python `repr`, and `FormatFloat(v, 'f', -1, 64)` produce, and the only safe path from a float to a decimal |
| Accumulation error | "float drift" | Error that grows as rounded results feed later operations; a property of the float format, fixed by changing to integers |
| Rounding bias | "banker's rounding fixes it" | Systematic drift from a rounding rule applied to ties; `HALF_UP` drifts up by half a unit per tie, `HALF_EVEN` averages to zero, regardless of number format |
| Two's-complement wrap | "integer overflow" | What Java `long` and Go `int64` do past 2^63-1: continue from -2^63 with no error; specified by both languages |
| Checked arithmetic | "`addExact`" | Operations that detect wrap and throw or return an error; `Math.addExact` in Java, hand-written in Go, a range check in Python |
| Fixed-point | "integer cents" | A decimal represented as an integer with an implied fixed number of decimal places; `Fixed2` has two, `Money` takes its scale from the currency |
| Compensated summation | "`fsum`" | Kahan or Neumaier summation that tracks the rounding error of each addition; Python's `sum()` of floats does this since 3.12 and hides the raw error |

## Further Reading

- [IEEE 754-2019 Standard for Floating-Point Arithmetic](https://standards.ieee.org/ieee/754/6210/) — the binary64 format and the rounding rules; section 3 for the formats, 4 for rounding-direction attributes.
- [Goldberg — What Every Computer Scientist Should Know About Floating-Point Arithmetic (1991)](https://docs.oracle.com/cd/E19957-01/806-3568/ncg_goldberg.html) — why `0.1` is not representable, what a unit in the last place is, and how errors propagate through sums.
- [Java Language Specification 15.18.2 — Additive Operators on Numeric Types](https://docs.oracle.com/javase/specs/jls/se21/html/jls-15.html#jls-15.18.2) — the sentence that says integer overflow wraps and does not throw; `Math.addExact` exists because of it.
- [The Go Programming Language Specification — Integer overflow](https://go.dev/ref/spec#Integer_overflow) — the same guarantee for Go, plus the note that constant expressions are evaluated exactly.
- [Python documentation — Floating Point Arithmetic: Issues and Limitations](https://docs.python.org/3/tutorial/floatingpoint.html) — `repr`, `0.1 + 0.2`, and the pointer to `fsum` and `decimal`.
- [`java.math.BigDecimal` (Java 21 API)](https://docs.oracle.com/en/java/javase/21/docs/api/java.base/java/math/BigDecimal.html) — read the `BigDecimal(double)` constructor note and compare it with `valueOf(double)`.
