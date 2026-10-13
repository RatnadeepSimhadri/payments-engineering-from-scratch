"""Lesson: phases/10-money-and-ledgers/01-representing-money/docs/en.md
Representing Money: Minor Units, Currencies, and Rounding
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 4217 (currency codes and exponents); IEEE 754 for why binary floats cannot hold 0.10.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from functools import total_ordering


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: a three-letter code and the number of decimals in its minor unit."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        """10**exponent: 100 for USD, 1 for JPY, 1000 for KWD."""
        return 10 ** self.exponent


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)
GBP = Currency("GBP", 2)
INR = Currency("INR", 2)
JPY = Currency("JPY", 0)
KWD = Currency("KWD", 3)


class Rounding(Enum):
    """How to resolve a fractional minor unit."""

    HALF_EVEN = "half_even"  # banker's rounding: ties go to the even neighbour; unbiased
    HALF_UP = "half_up"      # ties go away from zero; what most people mean by "round"
    DOWN = "down"            # truncate toward zero
    UP = "up"                # away from zero


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    """Integer division with an explicit rounding rule. Python's // floors toward -inf, so we
    work on magnitudes and reapply the sign to get truncation semantics identical to Java/Go."""
    if divisor == 0:
        raise ZeroDivisionError("divisor must not be zero")
    sign = -1 if (dividend < 0) != (divisor < 0) else 1
    abs_dividend, abs_divisor = abs(dividend), abs(divisor)
    quotient, remainder = divmod(abs_dividend, abs_divisor)
    if remainder == 0:
        return sign * quotient
    twice = remainder * 2
    if rounding is Rounding.DOWN:
        pass
    elif rounding is Rounding.UP:
        quotient += 1
    elif rounding is Rounding.HALF_UP:
        if twice >= abs_divisor:
            quotient += 1
    elif rounding is Rounding.HALF_EVEN:
        if twice > abs_divisor or (twice == abs_divisor and quotient % 2 == 1):
            quotient += 1
    return sign * quotient


@total_ordering
@dataclass(frozen=True)
class Money:
    """An exact amount of one currency, stored as a signed count of minor units. Immutable.
    Python ints never overflow, so the overflow checks in the Java/Go versions are not needed here."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    @staticmethod
    def parse(text: str, currency: Currency) -> "Money":
        """Parse "12.34", "-0.5", "1200" exactly; reject more decimals than the currency allows."""
        s = text.strip()
        negative = s.startswith("-")
        if negative or s.startswith("+"):
            s = s[1:]
        if not s:
            raise ValueError("empty amount")
        whole, _, frac = s.partition(".")
        whole = whole or "0"
        if not whole.isdigit() or (frac and not frac.isdigit()):
            raise ValueError(f"not a decimal amount: {text!r}")
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows ({currency.exponent})")
        frac = frac.ljust(currency.exponent, "0")
        minor = int(whole) * currency.minor_units_per_major() + (int(frac) if frac else 0)
        return Money(-minor if negative else minor, currency)

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def negate(self) -> "Money":
        return Money(-self.minor_units, self.currency)

    def abs(self) -> "Money":
        return self.negate() if self.minor_units < 0 else self

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def times(self, numerator: int, denominator: int, rounding: Rounding) -> "Money":
        """Multiply by an exact rational (2.9% is 29/1000) and round once, the way you asked."""
        if denominator <= 0:
            raise ArithmeticError("denominator must be positive")
        return Money(divide_rounded(self.minor_units * numerator, denominator, rounding), self.currency)

    def allocate(self, *ratios: int) -> list["Money"]:
        """Split in the given ratios so the parts sum exactly to the whole (largest-remainder method)."""
        if not ratios:
            raise ValueError("need at least one ratio")
        if any(r < 0 for r in ratios):
            raise ValueError("ratios must be non-negative")
        total = sum(ratios)
        if total == 0:
            raise ValueError("ratios must not all be zero")
        magnitude = abs(self.minor_units)
        shares = [magnitude * r // total for r in ratios]
        remainders = [magnitude * r % total for r in ratios]
        leftover = magnitude - sum(shares)
        order = sorted(range(len(ratios)), key=lambda i: -remainders[i])  # stable: ties keep index order
        for i in order[:leftover]:
            shares[i] += 1
        sign = -1 if self.minor_units < 0 else 1
        return [Money(sign * s, self.currency) for s in shares]

    @staticmethod
    def sum(currency: Currency, *amounts: "Money") -> "Money":
        acc = Money(0, currency)
        for m in amounts:
            acc = acc.plus(m)
        return acc

    def __lt__(self, other: "Money") -> bool:
        self._require_same_currency(other)
        return self.minor_units < other.minor_units

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        scale = self.currency.minor_units_per_major()
        whole, frac = divmod(magnitude, scale)
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


def main() -> None:
    print("== Why floats lose money ==")
    print(f"0.10 + 0.20 as float = {0.10 + 0.20:.20f}")
    cents = 0.0
    for _ in range(1_000_000):
        cents += 0.01
    print(f"one million additions of 0.01 as float = {cents:.10f} (should be exactly 10000)")

    print("\n== Exact arithmetic in minor units ==")
    price = Money.parse("19.99", USD)
    print(f"{price} x 3 = {price.times(3, 1, Rounding.HALF_EVEN)}")

    print("\n== Fees: 2.9% + 30c, rounded how? ==")
    percentage = price.times(29, 1000, Rounding.HALF_EVEN)
    print(f"2.9% of {price} = {percentage}  (19.99 * 0.029 = 0.57971 -> 0.58)")
    print(f"fee = {percentage} + 0.30 USD = {percentage.plus(Money.of_minor(30, USD))}")
    twelve_and_a_half = Money.of_minor(125, USD)
    print(f"12.5c rounded HALF_EVEN = {twelve_and_a_half.times(1, 10, Rounding.HALF_EVEN)}, "
          f"HALF_UP = {twelve_and_a_half.times(1, 10, Rounding.HALF_UP)}")

    print("\n== Splitting 100.00 three ways without losing a cent ==")
    thirds = Money.parse("100.00", USD).allocate(1, 1, 1)
    print(f"{thirds} sum = {Money.sum(USD, *thirds)}")
    print(f"0.05 split 70/30 = {Money.parse('0.05', USD).allocate(70, 30)}")

    print("\n== Currencies with different exponents ==")
    print(f"{Money.parse('1200', JPY)}  {Money.parse('1.250', KWD)}")
    for action in (lambda: Money.parse("1.5", JPY), lambda: Money.parse("1.00", USD).plus(Money.parse("1.00", EUR))):
        try:
            action()
        except (ArithmeticError, CurrencyMismatchError) as exc:
            print(f"rejected: {exc}")


if __name__ == "__main__":
    main()
