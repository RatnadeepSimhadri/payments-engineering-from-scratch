"""Lesson: phases/02-algorithms/13-interval-problems/docs/en.md
Interval Problems: Merging, Scheduling, and Sweep Lines
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Kleinberg & Tardos, Algorithm Design (2005), ch. 4; Shamos & Hoey, "Geometric intersection problems"
(FOCS 1976) for the sweep line; ISO 4217 for Money. Python 3.11+, standard library only. Same design as the Java.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

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
    """ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3 or not all(c.isupper() for c in self.code):
            raise ValueError(f"currency code must be three uppercase letters: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


@dataclass(frozen=True)
class Money:
    """Exact amount in minor units of one currency. Subset of Phase 10, Lesson 01: what this lesson needs."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if self.currency is None:
            raise TypeError("currency")
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> Money:
        return Money(minor_units, currency)

    @staticmethod
    def zero(currency: Currency) -> Money:
        return Money(0, currency)

    def _require_same_currency(self, other: Money) -> None:
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")

    def plus(self, other: Money) -> Money:
        self._require_same_currency(other)
        return Money(_check_long(self.minor_units + other.minor_units), self.currency)

    def minus(self, other: Money) -> Money:
        self._require_same_currency(other)
        return Money(_check_long(self.minor_units - other.minor_units), self.currency)

    def compare_to(self, other: Money) -> int:
        self._require_same_currency(other)
        return compare(self.minor_units, other.minor_units)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole, frac = divmod(abs(self.minor_units), scale)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


@dataclass(frozen=True, order=True)
class Interval:
    """
    A half-open interval [start, end) on an integer timeline. [0, 10) and [10, 20) touch but do not overlap:
    every instant belongs to exactly one of two adjacent intervals. Ordered by start, then end.
    """

    start: int
    end: int

    def __post_init__(self) -> None:
        if self.end < self.start:
            raise ValueError(f"end {self.end} is before start {self.start}")

    def length(self) -> int:
        return self.end - self.start

    def is_empty(self) -> bool:
        return self.end == self.start

    def overlaps(self, o: Interval) -> bool:
        """True if the two intervals share at least one instant. Touching intervals do not."""
        return self.start < o.end and o.start < self.end

    def overlaps_or_touches(self, o: Interval) -> bool:
        """True if they overlap or touch, i.e. their union is a single interval."""
        return self.start <= o.end and o.start <= self.end

    def contains(self, t: int) -> bool:
        return self.start <= t < self.end

    def intersect(self, o: Interval) -> Interval:
        """The overlapping part, or an empty interval at the later start if there is none."""
        s, e = max(self.start, o.start), min(self.end, o.end)
        return Interval(s, s) if e < s else Interval(s, e)

    def compare_to(self, o: Interval) -> int:
        """Sort by start, then by end. The order every algorithm below relies on."""
        c = compare(self.start, o.start)
        return c if c != 0 else compare(self.end, o.end)

    def __str__(self) -> str:
        return f"[{self.start}, {self.end})"


@dataclass(frozen=True)
class Peak:
    """The result of a maximum-overlap sweep: how many intervals were open at once, and the first instant."""

    count: int
    at: int


class Intervals:
    """Interval algorithms on half-open integer intervals. Every method is pure and leaves its input untouched."""

    @staticmethod
    def merge(input_: list[Interval]) -> list[Interval]:
        """Merge overlapping or touching intervals into a disjoint, sorted list. O(n log n)."""
        out: list[Interval] = []
        for iv in sorted(input_):
            if iv.is_empty():
                continue
            if not out or not out[-1].overlaps_or_touches(iv):
                out.append(iv)
            else:
                last = out.pop()
                out.append(Interval(last.start, max(last.end, iv.end)))
        return out

    @staticmethod
    def coverage(input_: list[Interval]) -> int:
        """Total length covered by the union of the intervals: the sum of the merged lengths."""
        total = 0
        for iv in Intervals.merge(input_):
            total = _check_long(total + iv.length())
        return total

    @staticmethod
    def gaps(input_: list[Interval], range_: Interval) -> list[Interval]:
        """The parts of `range_` not covered by any input interval, in order."""
        out: list[Interval] = []
        cursor = range_.start
        for iv in Intervals.merge(input_):
            if iv.end <= range_.start:
                continue
            if iv.start >= range_.end:
                break
            if iv.start > cursor:
                out.append(Interval(cursor, iv.start))
            cursor = max(cursor, iv.end)
        if cursor < range_.end:
            out.append(Interval(cursor, range_.end))
        return out

    @staticmethod
    def max_overlap(input_: list[Interval]) -> Peak:
        """
        Sweep line: +1 at each start, -1 at each end, sorted by time with -1 before +1 at equal times
        (half-open), walked with a running count. O(n log n).
        """
        events: list[tuple[int, int]] = []
        for iv in input_:
            if iv.is_empty():
                continue
            events.append((iv.start, +1))
            events.append((iv.end, -1))
        events.sort(key=lambda e: (e[0], e[1]))  # -1 sorts before +1
        open_ = best = 0
        best_at = 0 if not input_ else LONG_MIN
        for time, delta in events:
            open_ += delta
            if open_ > best:
                best = open_
                best_at = time
        return Peak(best, 0 if best == 0 else best_at)

    @staticmethod
    def max_non_overlapping(input_: list[Interval]) -> list[Interval]:
        """Interval scheduling by earliest end time (Phase 2, Lesson 07); ties broken by start. O(n log n)."""
        chosen: list[Interval] = []
        last_end = LONG_MIN
        for iv in sorted(input_, key=lambda iv: (iv.end, iv.start)):
            if iv.is_empty():
                continue
            if iv.start >= last_end:
                chosen.append(iv)
                last_end = iv.end
        return chosen

    @staticmethod
    def stabbing(input_: list[Interval], t: int) -> list[Interval]:
        """All intervals containing instant t, in sorted order. Linear scan."""
        return sorted(iv for iv in input_ if iv.contains(t))


@dataclass(frozen=True)
class Hold:
    """A card authorization hold: an amount reserved on an account for a window of time."""

    id: str
    amount: Money
    window: Interval

    def __post_init__(self) -> None:
        if self.id is None:
            raise TypeError("id")
        if self.amount.minor_units <= 0:
            raise ValueError(f"hold amount must be positive: {self.amount}")


@dataclass(frozen=True)
class PeakExposure:
    amount: Money
    at: int
    holds_open: int


class HoldBook:
    """All holds against one account: exposure now, and worst-case simultaneous exposure (a weighted sweep)."""

    def __init__(self, currency: Currency) -> None:
        if currency is None:
            raise TypeError("currency")
        self._currency = currency
        self._holds: list[Hold] = []

    def add(self, h: Hold) -> HoldBook:
        if h.amount.currency != self._currency:
            raise ValueError(f"hold in {h.amount.currency.code} on a {self._currency.code} book")
        self._holds.append(h)
        return self

    def holds(self) -> tuple[Hold, ...]:
        return tuple(self._holds)

    def exposure_at(self, t: int) -> Money:
        """Sum of holds whose window contains t."""
        total = Money.zero(self._currency)
        for h in self._holds:
            if h.window.contains(t):
                total = total.plus(h.amount)
        return total

    def expired_by(self, t: int) -> list[Hold]:
        """Holds whose window ended at or before t without being captured: candidates to release."""
        return [h for h in self._holds if h.window.end <= t]

    def peak_exposure(self) -> PeakExposure:
        """+amount at each start, -amount at each end, releases before reservations at equal instants."""
        events: list[tuple[int, int, int]] = []
        for i, h in enumerate(self._holds):
            if h.window.is_empty():
                continue
            events.append((h.window.start, +1, i))
            events.append((h.window.end, -1, i))
        events.sort(key=lambda e: (e[0], e[1]))  # stable: equal (time, delta) keep insertion order, like List.sort
        open_ = best = Money.zero(self._currency)
        best_at = 0
        open_count = best_count = 0
        for time, delta, i in events:
            amt = self._holds[i].amount
            open_ = open_.plus(amt) if delta > 0 else open_.minus(amt)
            open_count += delta
            if open_.compare_to(best) > 0:
                best = open_
                best_at = time
                best_count = open_count
        return PeakExposure(best, best_at, best_count)

    def busy_periods(self) -> list[Interval]:
        """The union of all hold windows: when was anything at all held."""
        return Intervals.merge([h.window for h in self._holds])


def t(hh: int, mm: int) -> int:
    """Minutes after midnight, so the numbers stay readable."""
    return hh * 60 + mm


def sample_holds() -> HoldBook:
    usd = USD
    return (HoldBook(usd)
            .add(Hold("hotel", Money.of_minor(25_000, usd), Interval(t(9, 0), t(17, 0))))
            .add(Hold("fuel", Money.of_minor(12_500, usd), Interval(t(10, 30), t(11, 30))))
            .add(Hold("restaurant", Money.of_minor(8_000, usd), Interval(t(12, 0), t(13, 0))))
            .add(Hold("car-rental", Money.of_minor(50_000, usd), Interval(t(11, 0), t(15, 0))))
            .add(Hold("parking", Money.of_minor(1_500, usd), Interval(t(13, 0), t(14, 0))))
            .add(Hold("coffee", Money.of_minor(600, usd), Interval(t(17, 0), t(17, 15)))))


def jstr(x: Any) -> str:
    """List.toString: [a, b] with each element's str()."""
    if isinstance(x, list):
        return "[" + ", ".join(jstr(e) for e in x) + "]"
    return str(x)


def main() -> None:
    print("== Settlement windows: merge overlapping cutoffs ==")
    windows = [
        Interval(t(14, 0), t(15, 30)),  # scheme A cutoff window
        Interval(t(15, 0), t(16, 0)),  # scheme B
        Interval(t(16, 0), t(16, 45)),  # scheme C, touches B
        Interval(t(9, 0), t(10, 0)),  # morning batch
        Interval(t(18, 0), t(18, 0)),  # an empty placeholder
    ]
    print(f"input:  {jstr(windows)}")
    print(f"merged: {jstr(Intervals.merge(windows))}")
    print(f"coverage: {Intervals.coverage(windows)} minutes; gaps in 08:00-19:00: "
          f"{jstr(Intervals.gaps(windows, Interval(t(8, 0), t(19, 0))))}")

    print()
    print("== Max overlap: how many windows are open at once ==")
    p = Intervals.max_overlap(windows)
    print(f"peak {p.count} windows open, first reached at minute {p.at} ({p.at // 60}:{p.at % 60:02d})")

    print()
    print("== Interval scheduling: most non-overlapping maintenance slots ==")
    slots = [Interval(1, 4), Interval(3, 5), Interval(0, 6), Interval(5, 7), Interval(3, 9), Interval(5, 9),
             Interval(6, 10), Interval(8, 11), Interval(8, 12), Interval(2, 14), Interval(12, 16)]
    print(f"slots: {jstr(slots)}")
    print(f"chosen by earliest end: {jstr(Intervals.max_non_overlapping(slots))}")

    print()
    print("== Authorization holds on one card: exposure over the day ==")
    book = sample_holds()
    for h in book.holds():
        print(f"  {h.id:<11} {str(h.amount):>12}  {h.window}")
    print(f"exposure at 12:30 = {book.exposure_at(t(12, 30))}")
    print(f"exposure at 17:00 = {book.exposure_at(t(17, 0))}  (hotel released at 17:00, coffee starts: half-open)")
    peak = book.peak_exposure()
    print(f"peak exposure {peak.amount} with {peak.holds_open} holds open, first at minute {peak.at}")
    print(f"busy periods: {jstr(book.busy_periods())}")
    print(f"expired by 14:00: {jstr([h.id for h in book.expired_by(t(14, 0))])}")


if __name__ == "__main__":
    main()
