"""Lesson: phases/02-algorithms/02-binary-search-and-its-variants/docs/en.md
Binary Search and Its Variants
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Bentley, "Programming Pearls" ch. 4; Bloch, "Extra, Extra - Read All About It: Nearly All Binary
Searches and Mergesorts are Broken" (2006). Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Callable

INT_MAX = 2**31 - 1
LONG_MAX = 2**63 - 1
LONG_MIN = -(2**63)


def to_int32(x: int) -> int:
    """What a Java `int` holds after arithmetic that overflowed: wrap to 32-bit two's complement."""
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


def trunc_div(a: int, b: int) -> int:
    """Java's `/` on integers truncates toward zero; Python's `//` floors toward negative infinity."""
    q = abs(a) // abs(b)
    return q if (a < 0) == (b < 0) else -q


def add_exact(a: int, b: int) -> int:
    """Math.addExact for long: Python ints never overflow, so check the 64-bit range explicitly."""
    r = a + b
    if not LONG_MIN <= r <= LONG_MAX:
        raise ArithmeticError("long overflow")
    return r


def compare(a: int, b: int) -> int:
    return (a > b) - (a < b)


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """An exact amount of one currency as a signed count of minor units (Phase 10, Lesson 01)."""

    minor_units: int
    currency: Currency

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> Money:
        return Money(minor_units, currency)

    def compare_to(self, other: Money) -> int:
        if self.currency != other.currency:
            raise ValueError(f"cannot compare {self.currency.code} with {other.currency.code}")
        return compare(self.minor_units, other.minor_units)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole, frac = divmod(abs(self.minor_units), scale)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


class BinarySearch:
    """Every search here is the same loop over a half-open range [lo, hi); the variants differ only in the predicate."""

    @staticmethod
    def index_of(a: list[int], target: int) -> int:
        """Classic: index of `target` in a sorted list, or -(insertion_point + 1) if absent (the JDK convention)."""
        lo, hi = 0, len(a) - 1  # closed range [lo, hi]
        while lo <= hi:
            mid = lo + ((hi - lo) >> 1)  # the Java-safe form; Python ints cannot overflow anyway
            if a[mid] < target:
                lo = mid + 1
            elif a[mid] > target:
                hi = mid - 1
            else:
                return mid
        return -(lo + 1)

    @staticmethod
    def first_true(lo: int, hi: int, predicate: Callable[[int], bool]) -> int:
        """Smallest i in [lo, hi) with predicate(i) true, or hi if none. Predicate must be monotone."""
        while lo < hi:
            mid = lo + ((hi - lo) >> 1)
            if predicate(mid):
                hi = mid  # mid is a candidate: keep it in the range
            else:
                lo = mid + 1  # mid is false: the answer is strictly to the right
        return lo  # invariant: everything < lo is false, everything >= hi is true

    @staticmethod
    def first_true_long(lo: int, hi: int, predicate: Callable[[int], bool]) -> int:
        """Same primitive over a range of values rather than indices (search over an answer space)."""
        while lo < hi:
            mid = lo + ((hi - lo) >> 1)
            if predicate(mid):
                hi = mid
            else:
                lo = mid + 1
        return lo

    @staticmethod
    def lower_bound(a: list[int], target: int) -> int:
        """First index whose value is >= target."""
        return BinarySearch.first_true(0, len(a), lambda i: a[i] >= target)

    @staticmethod
    def upper_bound(a: list[int], target: int) -> int:
        """First index whose value is > target."""
        return BinarySearch.first_true(0, len(a), lambda i: a[i] > target)

    @staticmethod
    def count(a: list[int], target: int) -> int:
        return BinarySearch.upper_bound(a, target) - BinarySearch.lower_bound(a, target)

    @staticmethod
    def count_in_range(a: list[int], low: int, high: int) -> int:
        """Number of elements in the closed value range [low, high]."""
        return BinarySearch.upper_bound(a, high) - BinarySearch.lower_bound(a, low)

    @staticmethod
    def rotation_point(a: list[int]) -> int:
        """Index of the minimum in a sorted-then-rotated list of distinct values."""
        if not a:
            raise ValueError("empty array")
        last = a[-1]
        # "a[i] <= last" is false on the rotated prefix and true from the minimum on
        return BinarySearch.first_true(0, len(a), lambda i: a[i] <= last)

    @staticmethod
    def index_of_rotated(a: list[int], target: int) -> int:
        """Search a rotated sorted list without un-rotating it."""
        if not a:
            return -1
        pivot = BinarySearch.rotation_point(a)
        n = len(a)
        k = BinarySearch.first_true(0, n, lambda i: a[(pivot + i) % n] >= target)
        if k == n or a[(pivot + k) % n] != target:
            return -1
        return (pivot + k) % n

    @staticmethod
    def integer_sqrt(n: int) -> int:
        """Largest x >= 0 with x * x <= n: search over the answer space."""
        if n < 0:
            raise ArithmeticError("negative")
        # first x with x*x > n, minus one; the guard x > n // x mirrors the Java overflow-avoiding form
        first = BinarySearch.first_true_long(0, 3_037_000_500, lambda x: x > 0 and x > n // x)
        return first - 1


class CappedBatch:
    """Smallest per-item cap such that capping every line item keeps the batch total within a budget."""

    @staticmethod
    def total_with_cap(items: list[Money], cap_minor: int) -> Money:
        total = 0
        for m in items:
            total = add_exact(total, min(m.minor_units, cap_minor))
        return Money.of_minor(total, items[0].currency)

    @staticmethod
    def largest_cap_within(items: list[Money], budget: Money) -> int:
        """Largest cap (minor units) with total_with_cap <= budget; -1 if even a cap of 0 exceeds it."""
        if not items:
            raise ValueError("no items")
        for m in items:
            if m.currency != budget.currency:
                raise ValueError("currency mismatch")
        max_item = 0
        for m in items:
            max_item = max(max_item, m.minor_units)
        # predicate: "a cap of c already exceeds the budget"; false for small c, true for large c
        first_over = BinarySearch.first_true_long(
            0, max_item + 1, lambda c: CappedBatch.total_with_cap(items, c).compare_to(budget) > 0)
        return first_over - 1


def fmt(a: list[int]) -> str:
    return "[" + ", ".join(str(x) for x in a) + "]"


def main() -> None:
    fees = [30, 30, 30, 50, 75, 75, 100, 250, 250, 250, 250, 500]
    print(f"== Sorted fee schedule (cents): {fmt(fees)} ==")
    print(f"indexOf(75)       = {BinarySearch.index_of(fees, 75)}   (some index of a 75; which one is unspecified)")
    print(f"indexOf(60)       = {BinarySearch.index_of(fees, 60)}   (negative: insertion point is {-BinarySearch.index_of(fees, 60) - 1})")
    print(f"lowerBound(250)   = {BinarySearch.lower_bound(fees, 250)}   (first 250)")
    print(f"upperBound(250)   = {BinarySearch.upper_bound(fees, 250)}  (one past the last 250)")
    print(f"count(250)        = {BinarySearch.count(fees, 250)}")
    print(f"count(60)         = {BinarySearch.count(fees, 60)}   (absent: lowerBound == upperBound)")
    print(f"countInRange(30, 100) = {BinarySearch.count_in_range(fees, 30, 100)}")

    print()
    print("== Overflow-safe midpoint ==")
    lo, hi = 1_500_000_000, 2_000_000_000
    # Python would print 1750000000 for (lo + hi) // 2; to_int32 shows what a Java int holds after the sum
    print(f"(lo + hi) / 2            = {trunc_div(to_int32(lo + hi), 2)}   (wrapped negative)")
    print(f"lo + ((hi - lo) >>> 1)   = {lo + ((hi - lo) >> 1)}")

    print()
    print("== firstTrue over a monotone predicate ==")
    first_over_200 = BinarySearch.first_true(0, len(fees), lambda i: fees[i] > 200)
    print(f"first index with fee > 200: {first_over_200} (value {fees[first_over_200]})")
    print(f"none true -> returns hi:    {BinarySearch.first_true(0, len(fees), lambda i: fees[i] > 10_000)}")

    print()
    print("== Rotated sorted array ==")
    rotated = [40, 50, 60, 70, 10, 20, 30]
    print(f"array           = {fmt(rotated)}")
    print(f"rotationPoint   = {BinarySearch.rotation_point(rotated)} (index of the minimum)")
    print(f"indexOfRotated(20) = {BinarySearch.index_of_rotated(rotated, 20)}")
    print(f"indexOfRotated(65) = {BinarySearch.index_of_rotated(rotated, 65)}")

    print()
    print("== Search over the answer space ==")
    print(f"integerSqrt(1_000_000_007) = {BinarySearch.integer_sqrt(1_000_000_007)}")
    print(f"integerSqrt(Long.MAX_VALUE) = {BinarySearch.integer_sqrt(LONG_MAX)}")
    usd = USD
    payouts = [Money.of_minor(12_000, usd), Money.of_minor(4_500, usd), Money.of_minor(80_000, usd),
               Money.of_minor(2_575, usd), Money.of_minor(30_000, usd)]
    budget = Money.of_minor(60_000, usd)
    cap = CappedBatch.largest_cap_within(payouts, budget)
    print("payouts: 120.00, 45.00, 800.00, 25.75, 300.00; budget 600.00 USD")
    print(f"largest per-payout cap within budget = {Money.of_minor(cap, usd)}"
          f" -> total {CappedBatch.total_with_cap(payouts, cap)}"
          f"; one cent more -> {CappedBatch.total_with_cap(payouts, cap + 1)}")


if __name__ == "__main__":
    main()
