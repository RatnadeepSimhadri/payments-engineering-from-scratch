"""Lesson: phases/02-algorithms/04-recursion-and-backtracking/docs/en.md
Recursion and Backtracking
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Knuth, TAOCP Vol. 4A, Section 7.2.1 (generating all n-tuples, permutations, combinations);
Golomb & Baumert, "Backtrack Programming" (JACM 1965). Python 3.11+, standard library only. Same design as the Java.
"""

from __future__ import annotations

import sys
from contextlib import contextmanager
from dataclasses import dataclass
from typing import Any, Iterator, TypeVar

T = TypeVar("T")

LONG_MIN = -(2**63)
LONG_MAX = 2**63 - 1


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
        r = self.minor_units + o.minor_units
        if not LONG_MIN <= r <= LONG_MAX:  # Math.addExact: Python ints never overflow, so check by hand
            raise ArithmeticError("long overflow")
        return Money(r, self.currency)

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


class Counter:
    """Counts how many nodes of the search tree were visited: the honest measure of pruning."""

    def __init__(self) -> None:
        self.visited = 0


class Enumerate:
    """Exhaustive generation: every permutation and every subset, by recursion that undoes its own choices."""

    @staticmethod
    def permutations(items: list[T]) -> list[list[T]]:
        """All n! orderings of the elements, in lexicographic order of positions."""
        out: list[list[T]] = []
        Enumerate._permute(items, [False] * len(items), [], out)
        return out

    @staticmethod
    def _permute(items: list[T], used: list[bool], current: list[T], out: list[list[T]]) -> None:
        if len(current) == len(items):
            out.append(list(current))  # leaf: copy the path
            return
        for i in range(len(items)):
            if used[i]:
                continue
            used[i] = True
            current.append(items[i])  # choose
            Enumerate._permute(items, used, current, out)  # explore
            current.pop()
            used[i] = False  # un-choose: the backtrack step

    @staticmethod
    def subsets(items: list[T]) -> list[list[T]]:
        """All 2^n subsets, each as a list preserving the input order; the empty set first."""
        out: list[list[T]] = []
        Enumerate._subsets_from(items, 0, [], out)
        return out

    @staticmethod
    def _subsets_from(items: list[T], index: int, current: list[T], out: list[list[T]]) -> None:
        if index == len(items):
            out.append(list(current))
            return
        Enumerate._subsets_from(items, index + 1, current, out)  # branch 1: leave items[index] out
        current.append(items[index])
        Enumerate._subsets_from(items, index + 1, current, out)  # branch 2: take it
        current.pop()

    @staticmethod
    def combinations(items: list[T], k: int) -> list[list[T]]:
        """All C(n, k) combinations of size k, in lexicographic index order."""
        if k < 0 or k > len(items):
            raise ValueError(f"k out of range: {k}")
        out: list[list[T]] = []
        Enumerate._combine(items, k, 0, [], out)
        return out

    @staticmethod
    def _combine(items: list[T], k: int, start: int, current: list[T], out: list[list[T]]) -> None:
        if len(current) == k:
            out.append(list(current))
            return
        # prune: if even taking every remaining item cannot reach k, stop
        for i in range(start, len(items) - (k - len(current)) + 1):
            current.append(items[i])
            Enumerate._combine(items, k, i + 1, current, out)
            current.pop()


@dataclass(frozen=True)
class Frame:
    """One invocation's locals for the explicit-stack search: what the call stack would have held."""

    index: int
    sum: int
    chosen: list


class SettlementAllocator:
    """
    Enumerate every subset of pending receivables whose amounts sum exactly to the settlement target.
    Pruning: sort descending, stop a branch when the running sum exceeds the target, and stop when the
    remaining items cannot reach it. Still exponential in the worst case; that is the lesson.
    """

    @staticmethod
    def allocations(receivables: list[Money], target: Money, prune: bool, counter: Counter) -> list[list[Money]]:
        ccy = target.currency
        for m in receivables:
            if m.currency != ccy:
                raise ValueError("mixed currencies")
            if m.minor_units <= 0:
                raise ValueError("receivables must be positive")
        sorted_ = sorted(receivables, key=lambda m: -m.minor_units)  # largest first: prune earlier (stable, as List.sort)
        suffix = [0] * (len(sorted_) + 1)  # suffix[i] = sum of sorted_[i:]
        for i in range(len(sorted_) - 1, -1, -1):
            suffix[i] = suffix[i + 1] + sorted_[i].minor_units
        out: list[list[Money]] = []
        SettlementAllocator._search(sorted_, suffix, 0, 0, target.minor_units, [], out, prune, counter)
        return out

    @staticmethod
    def _search(items: list[Money], suffix: list[int], index: int, sum_: int, target: int,
                chosen: list[Money], out: list[list[Money]], prune: bool, counter: Counter) -> None:
        counter.visited += 1
        if sum_ == target:
            out.append(list(chosen))  # found one; nothing more can be added (positive amounts)
            return
        if index == len(items):
            return
        if prune:
            if sum_ > target:
                return  # overshot: no positive amount can fix it
            if sum_ + suffix[index] < target:
                return  # even taking everything left falls short
        item = items[index]
        chosen.append(item)
        SettlementAllocator._search(items, suffix, index + 1, sum_ + item.minor_units, target,
                                    chosen, out, prune, counter)
        chosen.pop()
        SettlementAllocator._search(items, suffix, index + 1, sum_, target, chosen, out, prune, counter)

    @staticmethod
    def allocations_iterative(receivables: list[Money], target: Money, counter: Counter) -> list[list[Money]]:
        """Same search, no recursion: an explicit stack of frames. Same allocations in the same order."""
        sorted_ = sorted(receivables, key=lambda m: -m.minor_units)
        suffix = [0] * (len(sorted_) + 1)
        for i in range(len(sorted_) - 1, -1, -1):
            suffix[i] = suffix[i + 1] + sorted_[i].minor_units
        t = target.minor_units

        out: list[list[Money]] = []
        stack: list[Frame] = [Frame(0, 0, [])]
        while stack:
            f = stack.pop()
            counter.visited += 1
            if f.sum == t:
                out.append(f.chosen)
                continue
            if f.index == len(sorted_) or f.sum > t or f.sum + suffix[f.index] < t:
                continue
            # push the "skip" branch first so the "take" branch is popped (explored) first, matching the recursion
            stack.append(Frame(f.index + 1, f.sum, f.chosen))
            taken = f.chosen + [sorted_[f.index]]
            stack.append(Frame(f.index + 1, f.sum + sorted_[f.index].minor_units, taken))
        return out


class NQueens:
    """N-Queens: the classic constraint-satisfaction backtrack, with a visit counter to show what pruning buys."""

    @staticmethod
    def count_solutions(n: int, counter: Counter) -> int:
        return NQueens._place(n, 0, [False] * n, [False] * (2 * n), [False] * (2 * n), counter)

    @staticmethod
    def _place(n: int, row: int, cols: list[bool], diag1: list[bool], diag2: list[bool], counter: Counter) -> int:
        counter.visited += 1
        if row == n:
            return 1
        found = 0
        for col in range(n):
            d1, d2 = row + col, row - col + n
            if cols[col] or diag1[d1] or diag2[d2]:
                continue  # constraint check before descending: the prune
            cols[col] = diag1[d1] = diag2[d2] = True
            found += NQueens._place(n, row + 1, cols, diag1, diag2, counter)
            cols[col] = diag1[d1] = diag2[d2] = False
        return found


class Depth:
    """Recursion depth is a resource. Python's limit is a counter (sys.getrecursionlimit), not the thread's stack size."""

    @staticmethod
    def sum_recursive(n: int) -> int:
        return 0 if n == 0 else n + Depth.sum_recursive(n - 1)

    @staticmethod
    def sum_iterative(n: int) -> int:
        s = 0
        for i in range(1, n + 1):
            s += i
        return s

    @staticmethod
    def measure_max_depth() -> int:
        """Depth at which this call chain hits RecursionError for a trivial frame; measured, not assumed."""
        depth = [0]
        try:
            Depth._probe(depth)
        except RecursionError:
            pass  # expected
        return depth[0]

    @staticmethod
    def _probe(depth: list[int]) -> None:
        depth[0] += 1
        Depth._probe(depth)

    @staticmethod
    @contextmanager
    def recursion_limit(limit: int) -> Iterator[None]:
        """Temporarily set sys.setrecursionlimit; the Python equivalent of running with a different -Xss."""
        old = sys.getrecursionlimit()
        sys.setrecursionlimit(limit)
        try:
            yield
        finally:
            sys.setrecursionlimit(old)


def jstr(x: Any) -> str:
    """List.toString: [a, b] with elements' str(), nested lists included."""
    if isinstance(x, list):
        return "[" + ", ".join(jstr(e) for e in x) + "]"
    return str(x)


def main() -> None:
    usd = USD

    print("== Permutations of [A, B, C] ==")
    for p in Enumerate.permutations(["A", "B", "C"]):
        print(f"  {jstr(p)}")
    print("== Subsets of [A, B, C] ==")
    print(f"  {jstr(Enumerate.subsets(['A', 'B', 'C']))}")
    print("== Combinations C(4, 2) of [A, B, C, D] ==")
    print(f"  {jstr(Enumerate.combinations(['A', 'B', 'C', 'D'], 2))}")

    print()
    print("== Settlement allocation: which receivables exactly cover 120.00 USD? ==")
    receivables = [Money.of_minor(4_500, usd), Money.of_minor(7_500, usd), Money.of_minor(2_000, usd),
                   Money.of_minor(12_000, usd), Money.of_minor(5_500, usd), Money.of_minor(1_000, usd),
                   Money.of_minor(3_000, usd)]
    target = Money.of_minor(12_000, usd)
    no_prune, with_prune, iterative = Counter(), Counter(), Counter()
    a1 = SettlementAllocator.allocations(receivables, target, False, no_prune)
    a2 = SettlementAllocator.allocations(receivables, target, True, with_prune)
    a3 = SettlementAllocator.allocations_iterative(receivables, target, iterative)
    print(f"receivables: {jstr(receivables)}")
    for alloc in a2:
        print(f"  {jstr(alloc)}")
    same = "true" if a1 == a2 and a2 == a3 else "false"
    print(f"{len(a2)} allocations; nodes visited: no pruning {no_prune.visited}, pruning {with_prune.visited}, "
          f"iterative {iterative.visited}; same results: {same}")

    print()
    print("== Pruning at scale: 24 receivables, one target ==")
    many = [Money.of_minor(100 * i * (i % 5 + 1), usd) for i in range(1, 25)]  # 24 distinct amounts
    big = Money.of_minor(50_000, usd)
    c1, c2 = Counter(), Counter()
    n1 = len(SettlementAllocator.allocations(many, big, False, c1))
    n2 = len(SettlementAllocator.allocations(many, big, True, c2))
    print(f"{n2} allocations found both ways; nodes visited without pruning: {c1.visited:,} "
          f"(2^25 - 1 = {(1 << 25) - 1:,}), with pruning: {c2.visited:,}")
    if n1 != n2:
        raise AssertionError("pruning changed the answer")

    print()
    print("== N-Queens: constraint checks before descending ==")
    for n in (4, 6, 8):
        c = Counter()
        solutions = NQueens.count_solutions(n, c)
        print(f"  n={n}: {solutions} solutions, {c.visited:,} nodes visited (of {n ** n:,} leaves in the unpruned tree)")

    print()
    print("== Recursion depth is finite ==")
    max_depth = Depth.measure_max_depth()
    with Depth.recursion_limit(5_000):  # 1,000 frames is already past CPython's default limit
        recursive = Depth.sum_recursive(1_000)
    print(f"sumRecursive(1_000) = {recursive}, sumIterative(10_000_000) = {Depth.sum_iterative(10_000_000)}")
    print(f"this thread overflowed at a recursion depth of about {max_depth} trivial frames "
          f"(sys.getrecursionlimit() = {sys.getrecursionlimit()}; sys.setrecursionlimit changes it)")
    print("a recursive sum over a day's 2,000,000 ledger lines would not survive; the explicit-stack version does not care")


if __name__ == "__main__":
    main()
