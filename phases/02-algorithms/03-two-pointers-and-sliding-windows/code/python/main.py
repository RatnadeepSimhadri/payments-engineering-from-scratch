"""Lesson: phases/02-algorithms/03-two-pointers-and-sliding-windows/docs/en.md
Two Pointers and Sliding Windows
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Cormen et al., Introduction to Algorithms (amortized analysis of two-index scans); the monotonic deque
for sliding-window maximum follows the standard formulation (e.g. Tarjan, "Amortized Computational Complexity").
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass
from enum import Enum

LONG_MIN = -(2**63)
LONG_MAX = 2**63 - 1


def _check_long(r: int) -> int:
    """Math.addExact / subtractExact: Python ints never overflow, so check the signed 64-bit range explicitly."""
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

    def plus(self, o: Money) -> Money:
        self._require_same(o)
        return Money(_check_long(self.minor_units + o.minor_units), self.currency)

    def minus(self, o: Money) -> Money:
        self._require_same(o)
        return Money(_check_long(self.minor_units - o.minor_units), self.currency)

    def _require_same(self, o: Money) -> None:
        if self.currency != o.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {o.currency.code}")

    def compare_to(self, other: Money) -> int:
        self._require_same(other)
        return compare(self.minor_units, other.minor_units)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole, frac = divmod(abs(self.minor_units), scale)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


@dataclass(frozen=True)
class Event:
    """One card authorization attempt: when it happened (ms since epoch) and for how much. Events arrive in time order."""

    timestamp_ms: int
    amount: Money


class TwoPointers:
    """Two indices moving over one sorted list."""

    @staticmethod
    def pair_with_sum(sorted_: list[int], target: int) -> list[int] | None:
        """Indices [i, j], i < j, with a[i] + a[j] == target in a sorted list, or None. O(n)."""
        i, j = 0, len(sorted_) - 1
        while i < j:
            s = sorted_[i] + sorted_[j]
            if s == target:
                return [i, j]
            if s < target:
                i += 1  # the smallest element can never pair with anything: drop it
            else:
                j -= 1  # the largest element can never pair with anything: drop it
        return None

    @staticmethod
    def dedupe_sorted(sorted_: list[int]) -> int:
        """Remove duplicates from a sorted list in place; returns the new length. Reader and writer pointers."""
        if not sorted_:
            return 0
        write = 1
        for read in range(1, len(sorted_)):
            if sorted_[read] != sorted_[write - 1]:
                sorted_[write] = sorted_[read]
                write += 1
        return write

    @staticmethod
    def merge_sorted(a: list[int], b: list[int]) -> list[int]:
        """Merge two sorted lists into one sorted list. The merge step from Lesson 01, standing alone."""
        out: list[int] = []
        i = j = 0
        while i < len(a) and j < len(b):
            if a[i] <= b[j]:
                out.append(a[i])
                i += 1
            else:
                out.append(b[j])
                j += 1
        out.extend(a[i:])
        out.extend(b[j:])
        return out


class SlidingWindow:
    """Windows over a list or an event stream. Each method is O(n) total because both ends only move forward."""

    @staticmethod
    def max_sum_fixed(a: list[int], k: int) -> int:
        """Fixed width k: the maximum sum of any k consecutive elements. Raises if k is out of range."""
        if k <= 0 or k > len(a):
            raise ValueError(f"k must be in 1..{len(a)}: {k}")
        s = sum(a[:k])
        best = s
        for right in range(k, len(a)):
            s += a[right] - a[right - k]  # slide: add the new element, drop the one that fell off the left
            best = max(best, s)
        return best

    @staticmethod
    def shortest_run_with_sum_at_least(a: list[int], target: int) -> int:
        """Variable width: length of the shortest contiguous run with sum >= target; 0 if none. Non-negative only."""
        for v in a:
            if v < 0:
                raise ValueError("values must be non-negative for the window to be monotone")
        best = left = 0
        s = 0
        for right in range(len(a)):
            s += a[right]
            while s >= target and left <= right:  # shrink from the left while the window still qualifies
                length = right - left + 1
                if best == 0 or length < best:
                    best = length
                s -= a[left]
                left += 1
        return best

    @staticmethod
    def longest_without_repeat(s: str) -> int:
        """Longest substring with no repeated character; the "at most one of each" window."""
        last_seen: dict[str, int] = {}
        best = left = 0
        for right, c in enumerate(s):
            if last_seen.get(c, -1) >= left:
                left = last_seen[c] + 1  # jump left past the previous copy of c
            last_seen[c] = right
            best = max(best, right - left + 1)
        return best

    @staticmethod
    def max_of_each_window(a: list[int], k: int) -> list[int]:
        """Sliding-window maximum with a monotonic deque of indices whose values strictly decrease front to back."""
        if k <= 0 or k > len(a):
            raise ValueError(f"k must be in 1..{len(a)}: {k}")
        out = [0] * (len(a) - k + 1)
        dq: deque[int] = deque()
        for i in range(len(a)):
            while dq and a[dq[-1]] <= a[i]:
                dq.pop()  # a[i] dominates everything smaller before it
            dq.append(i)
            if dq[0] <= i - k:
                dq.popleft()  # the front fell out of the window
            if i >= k - 1:
                out[i - k + 1] = a[dq[0]]
        return out


class Decision(Enum):
    ALLOW = "ALLOW"
    DENY_COUNT = "DENY_COUNT"
    DENY_AMOUNT = "DENY_AMOUNT"


class VelocityChecker:
    """
    "No more than max_count authorizations and no more than max_total in any trailing window of window_ms".
    Events are fed in timestamp order; the window is the last window_ms of events, inclusive of the newest.
    """

    Decision = Decision

    def __init__(self, window_ms: int, max_count: int, max_total: Money) -> None:
        if window_ms <= 0 or max_count <= 0:
            raise ValueError("window and count must be positive")
        self._window_ms = window_ms
        self._max_count = max_count
        self._max_total = max_total
        self._window: deque[Event] = deque()
        self._total = Money.of_minor(0, max_total.currency)
        self._last_timestamp = LONG_MIN

    def record(self, e: Event) -> Decision:
        """Record the event and decide. Denied events still count: an attacker's failed attempts are the signal."""
        if e.timestamp_ms < self._last_timestamp:
            raise ValueError("events must arrive in time order")
        self._last_timestamp = e.timestamp_ms
        self._evict_older_than(e.timestamp_ms - self._window_ms)
        self._window.append(e)
        self._total = self._total.plus(e.amount)
        if len(self._window) > self._max_count:
            return Decision.DENY_COUNT
        if self._total.compare_to(self._max_total) > 0:
            return Decision.DENY_AMOUNT
        return Decision.ALLOW

    def _evict_older_than(self, cutoff_exclusive: int) -> None:
        while self._window and self._window[0].timestamp_ms <= cutoff_exclusive:
            self._total = self._total.minus(self._window.popleft().amount)

    def count_in_window(self) -> int:
        return len(self._window)

    def total_in_window(self) -> Money:
        return self._total


def fmt(a: list[int] | None) -> str:
    """Arrays.toString: "[1, 2]" or "null"."""
    return "null" if a is None else "[" + ", ".join(str(x) for x in a) + "]"


def main() -> None:
    usd = USD

    print("== Two pointers on a sorted array ==")
    amounts = [75, 500, 500, 1999, 1999, 2575, 4500, 12000]
    pair = TwoPointers.pair_with_sum(amounts, 4574)
    print(f"pairWithSum(4574) -> indices {fmt(pair)} = {amounts[pair[0]]} + {amounts[pair[1]]}")
    print(f"pairWithSum(1)    -> {fmt(TwoPointers.pair_with_sum(amounts, 1))}")
    copy = list(amounts)
    n = TwoPointers.dedupe_sorted(copy)
    print(f"dedupeSorted      -> {fmt(copy[:n])}")
    print(f"mergeSorted       -> {fmt(TwoPointers.merge_sorted([1, 4, 7], [2, 3, 9]))}")

    print()
    print("== Fixed window: best 3-day settlement total (cents per day) ==")
    daily = [12000, 4500, 80000, 2575, 30000, 1999, 500, 75]
    print(f"days: {fmt(daily)}")
    print(f"maxSumFixed(k=3) = {SlidingWindow.max_sum_fixed(daily, 3)} (days 2..4: 80000 + 2575 + 30000)")

    print()
    print("== Variable window: shortest run of days totalling at least 100,000 cents ==")
    print(f"shortestRunWithSumAtLeast(100000) = {SlidingWindow.shortest_run_with_sum_at_least(daily, 100_000)} days")
    print(f"shortestRunWithSumAtLeast(1000000) = "
          f"{SlidingWindow.shortest_run_with_sum_at_least(daily, 1_000_000)} (impossible -> 0)")
    print(f'longestWithoutRepeat("abcabcbb") = {SlidingWindow.longest_without_repeat("abcabcbb")}')

    print()
    print("== Sliding-window maximum with a monotonic deque, k = 3 ==")
    prices = [1, 3, -1, -3, 5, 3, 6, 7]
    print(f"input  {fmt(prices)}")
    print(f"maxima {fmt(SlidingWindow.max_of_each_window(prices, 3))}")

    print()
    print("== Velocity check: max 4 authorizations and 500.00 USD per trailing 60 s ==")
    vc = VelocityChecker(60_000, 4, Money.of_minor(50_000, usd))
    t0 = 1_700_000_000_000
    script = [
        (t0 + 0, 12_000), (t0 + 10_000, 4_500), (t0 + 20_000, 25_000), (t0 + 25_000, 1_999),
        (t0 + 30_000, 100), (t0 + 70_000, 30_000), (t0 + 125_000, 75),
    ]
    for ts, minor in script:
        e = Event(ts, Money.of_minor(minor, usd))
        d = vc.record(e)
        print(f"  t=+{(ts - t0) // 1000:3d}s  {str(e.amount):<10} -> {d.name:<12} "
              f"window: {vc.count_in_window()} events, {vc.total_in_window()}")


if __name__ == "__main__":
    main()
