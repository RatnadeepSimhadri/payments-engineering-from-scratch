"""Lesson: phases/00-setup-and-tooling/07-decimal-and-integer-arithmetic/docs/en.md
Decimal and Integer Arithmetic in Three Runtimes: floats, unbounded ints, int64 range checks, and a Fixed2 type
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: IEEE 754-2019 (binary64); Goldberg, "What Every Computer Scientist Should Know About Floating-Point
         Arithmetic" (1991); Python docs "Floating Point Arithmetic: Issues and Limitations"; the decimal module.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal
from enum import Enum

INT64_MIN = -(1 << 63)
INT64_MAX = (1 << 63) - 1
_MASK64 = (1 << 64) - 1


class Rounding(Enum):
    """How to resolve a fraction when integer division is inexact. Same four modes as Phase 10, Lesson 01."""

    HALF_EVEN = "half_even"
    HALF_UP = "half_up"
    DOWN = "down"
    UP = "up"


# ---- Floats: where they go wrong, isolated so the tests can pin each failure ----

def add_floats(a: float, b: float) -> float:
    return a + b


def naive_cents(amount: float) -> int:
    """The penny-skimming bug: int(19.99 * 100) is 1998 because 19.99 * 100 is really 1998.9999999999998."""
    return int(amount * 100)


def sum_repeated(term: float, times: int) -> float:
    """Add `term` to 0.0 `times` times with an explicit loop. Each addition rounds; the errors do not cancel.
    Deliberately not sum(): since Python 3.12, sum() of floats uses compensated (Neumaier) summation and
    hides exactly the error this lesson is about. math.fsum has always done so."""
    acc = 0.0
    for _ in range(times):
        acc += term
    return acc


# ---- Checked arithmetic: Python ints never overflow, so the check is against the int64 range. ----
# A Python service that writes to a BIGINT column, or talks to a Java or Go service, has the same 2^63 limit;
# it just does not find out at the arithmetic. These functions make the limit explicit so the tests match.

def _require_int64(value: int, what: str) -> int:
    if value < INT64_MIN or value > INT64_MAX:
        raise OverflowError(f"int64 overflow: {what}")
    return value


def checked_add(a: int, b: int) -> int:
    return _require_int64(a + b, f"{a} + {b}")


def checked_sub(a: int, b: int) -> int:
    return _require_int64(a - b, f"{a} - {b}")


def checked_mul(a: int, b: int) -> int:
    return _require_int64(a * b, f"{a} * {b}")


def checked_negate(a: int) -> int:
    return _require_int64(-a, f"-({a})")


def wrapping_add(a: int, b: int) -> int:
    """What Java and Go do with plain +: two's-complement wrap. Emulated here so the three demos print the same."""
    u = (a + b) & _MASK64
    return u - (1 << 64) if u > INT64_MAX else u


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    """Integer division with an explicit rounding rule; identical to Phase 10, Lesson 01. Python's // floors
    toward -inf while Java and Go truncate toward zero, so this works on magnitudes and reapplies the sign."""
    if divisor == 0:
        raise ZeroDivisionError("division by zero")
    sign = -1 if (dividend < 0) != (divisor < 0) else 1
    quotient, remainder = divmod(abs(dividend), abs(divisor))
    if remainder == 0:
        return sign * quotient
    twice = remainder * 2
    abs_divisor = abs(divisor)
    if rounding is Rounding.UP:
        quotient += 1
    elif rounding is Rounding.HALF_UP:
        if twice >= abs_divisor:
            quotient += 1
    elif rounding is Rounding.HALF_EVEN:
        if twice > abs_divisor or (twice == abs_divisor and quotient % 2 == 1):
            quotient += 1
    return sign * quotient


def rounding_bias(rounding: Rounding) -> int:
    """Round each of 0.5, 1.5, ..., 99.5 (stored as tenths: 5, 15, ..., 995) to an integer with the given mode
    and return the sum minus the exact sum (5000). HALF_UP drifts by +50 over 100 ties; HALF_EVEN by 0."""
    total = sum(divide_rounded(tenths, 10, rounding) for tenths in range(5, 1000, 10))
    return total - 5000


# ---- Fixed2: a fixed-point decimal with exactly two places, stored as hundredths ----

@dataclass(frozen=True, order=True)
class Fixed2:
    """No currency: that, plus allocation, is what Phase 10, Lesson 01's Money adds on top of this type.
    Every operation that could produce a fraction takes a Rounding. Results outside int64 are an error, so the
    type behaves the same as its Java and Go twins even though Python would happily keep going."""

    hundredths: int

    def __post_init__(self) -> None:
        if not isinstance(self.hundredths, int) or isinstance(self.hundredths, bool):
            raise TypeError("hundredths must be an int; never construct Fixed2 from a float")
        _require_int64(self.hundredths, f"Fixed2({self.hundredths})")

    @staticmethod
    def of_hundredths(hundredths: int) -> "Fixed2":
        return Fixed2(hundredths)

    @staticmethod
    def parse(text: str) -> "Fixed2":
        """Parse "19.99", "-0.5", "7" exactly. More than two decimals is an error, not a rounding."""
        s = text.strip()
        negative = s.startswith("-")
        if negative or s.startswith("+"):
            s = s[1:]
        if not s:
            raise ValueError("empty amount")
        whole, _, frac = s.partition(".")
        whole = whole or "0"
        if not whole.isdigit() or (frac and not frac.isdigit()):
            raise ValueError(f"not a decimal: {text!r}")
        if len(frac) > 2:
            raise ArithmeticError(f"{text} has more than two decimals")
        value = checked_add(checked_mul(int(whole), 100), int(frac.ljust(2, "0")))
        return Fixed2(-value if negative else value)

    @staticmethod
    def from_double(value: float) -> "Fixed2":
        """The only correct way in from a float: through its shortest round-trip decimal string (repr), never
        through int(v * 100). 19.99 -> "19.99" -> 1999; 19.995 is rejected like any 3-decimal input."""
        if value != value or value in (float("inf"), float("-inf")):
            raise ArithmeticError(f"not finite: {value}")
        return Fixed2.parse(format(Decimal(repr(value)), "f"))  # repr is the shortest string that round-trips

    def plus(self, other: "Fixed2") -> "Fixed2":
        return Fixed2(checked_add(self.hundredths, other.hundredths))

    def minus(self, other: "Fixed2") -> "Fixed2":
        return Fixed2(checked_sub(self.hundredths, other.hundredths))

    def negate(self) -> "Fixed2":
        return Fixed2(checked_negate(self.hundredths))

    def times(self, other: "Fixed2", rounding: Rounding) -> "Fixed2":
        """Multiply two Fixed2 values: the product has four decimals, so two must be rounded away, your way."""
        product = checked_mul(self.hundredths, other.hundredths)  # scaled by 10^4
        return Fixed2(divide_rounded(product, 100, rounding))

    def times_rate(self, numerator: int, denominator: int, rounding: Rounding) -> "Fixed2":
        """Multiply by an exact rational: 2.9% is 29/1000, an FX rate of 0.9217 is 9217/10000."""
        if denominator <= 0:
            raise ArithmeticError("denominator must be positive")
        return Fixed2(divide_rounded(checked_mul(self.hundredths, numerator), denominator, rounding))

    def divided_by(self, divisor: int, rounding: Rounding) -> "Fixed2":
        return Fixed2(divide_rounded(self.hundredths, divisor, rounding))

    def __str__(self) -> str:
        """"19.99", "-0.05", "0.00": always two decimals, never scientific notation."""
        magnitude = abs(self.hundredths)
        return f"{'-' if self.hundredths < 0 else ''}{magnitude // 100}.{magnitude % 100:02d}"

    __repr__ = __str__


ZERO = Fixed2(0)


def main() -> None:
    print("== 0.1 + 0.2 ==")
    a, b = 0.1, 0.2
    print(f"0.1 + 0.2 = {add_floats(a, b)!r}  (equals 0.3? {str(add_floats(a, b) == 0.3).lower()})")
    print(f"int(19.99 * 100) = {naive_cents(19.99)}  <- the penny-skimming bug (19.99 * 100 = 1998.9999999999998)")
    print(f"Fixed2.from_double(19.99) = {Fixed2.from_double(19.99)}  (via the shortest decimal string \"19.99\")")

    print("\n== Accumulation ==")
    print(f"0.1 added 10 times as float = {sum_repeated(0.1, 10)!r}")
    million = sum_repeated(0.01, 1_000_000)
    print(f"0.01 added 1,000,000 times as float = {million:.10f} (error {million - 10_000:.3e})")
    acc = ZERO
    cent = Fixed2.parse("0.01")
    for _ in range(1_000_000):
        acc = acc.plus(cent)
    print(f"0.01 added 1,000,000 times as Fixed2 = {acc} exactly")

    print("\n== Rounding bias on 100 exact half-ties (0.5, 1.5, ..., 99.5) ==")
    for r in Rounding:
        print(f"{r.name:<9} drift = {rounding_bias(r):+d}")

    print("\n== Overflow ==")
    print(f"2**63 - 1 + 1 with plain + = {INT64_MAX + 1}  (Python ints do not wrap)")
    print(f"the same in Java/Go, emulated: wrapping_add = {wrapping_add(INT64_MAX, 1)}  (wrapped to INT64_MIN)")
    try:
        checked_add(INT64_MAX, 1)
    except OverflowError as exc:
        print(f"checked_add(INT64_MAX, 1) -> OverflowError: {exc}")

    print("\n== Fixed2: two decimals, explicit rounding ==")
    price = Fixed2.parse("19.99")
    print(f"{price} x 3        = {price.times(Fixed2.parse('3'), Rounding.HALF_EVEN)}")
    print(f"{price} x 2.9%     = {price.times_rate(29, 1000, Rounding.HALF_EVEN)}  (57.971 hundredths -> 58)")
    print(f"{price} x 1.08     = {price.times(Fixed2.parse('1.08'), Rounding.HALF_EVEN)}  (2158.92 hundredths -> 2159)")
    third = Fixed2.parse("100.00").divided_by(3, Rounding.HALF_EVEN)
    print(f"100.00 / 3       = {third}, and {third} x 3 = {third.times(Fixed2.parse('3'), Rounding.HALF_EVEN)}"
          "  <- why Phase 10 adds allocate()")
    try:
        Fixed2.parse("19.995")
    except ArithmeticError as exc:
        print(f"rejected: {exc}")

    print("\n== Decimal types in the standard library ==")
    print(f"Decimal('0.1') + Decimal('0.2') = {Decimal('0.1') + Decimal('0.2')}")
    print(f"Decimal(0.1)  (from a float)    = {Decimal(0.1)}")


if __name__ == "__main__":
    main()
