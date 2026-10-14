"""Lesson: phases/02-algorithms/01-sorting-algorithms/docs/en.md
Sorting: Merge, Quick, Heap, and Why Libraries Use Timsort
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Knuth, TAOCP Vol. 3; Bentley & McIlroy, "Engineering a Sort Function" (1993); Peters, "Timsort" (CPython listsort.txt).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from typing import Any, Callable

Comparator = Callable[[Any, Any], int]


class JavaRandom:
    """Bit-for-bit port of java.util.Random (48-bit LCG) so a seed yields the same sequence as the Java demo."""

    _MULT = 0x5DEECE66D
    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULT) & self._MASK

    def _next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULT + 0xB) & self._MASK
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r  # Java's (int) cast: only next(32) can go negative

    def next_int(self, bound: int | None = None) -> int:
        if bound is None:
            return self._next(32)
        if bound <= 0:
            raise ValueError("bound must be positive")
        if bound & (bound - 1) == 0:
            return (bound * self._next(31)) >> 31
        while True:
            bits = self._next(31)
            val = bits % bound
            if bits - val + (bound - 1) < (1 << 31):  # Java's int-overflow rejection test
                return val


def compare(a: int, b: int) -> int:
    """Long.compare / Integer.compare."""
    return (a > b) - (a < b)


def natural_order(a: Any, b: Any) -> int:
    return compare(a, b)


def then_comparing(first: Comparator, second: Comparator) -> Comparator:
    """Comparator.thenComparing: break ties of `first` with `second`."""
    def cmp(a: Any, b: Any) -> int:
        c = first(a, b)
        return c if c != 0 else second(a, b)
    return cmp


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


@dataclass(frozen=True)
class Transaction:
    """A ledger line: the sequence number is the arrival order, which a stable sort by amount must preserve."""

    seq: int
    account: str
    amount: Money

    @staticmethod
    def BY_AMOUNT(a: Transaction, b: Transaction) -> int:  # noqa: N802 (mirrors the Java constant)
        return a.amount.compare_to(b.amount)

    @staticmethod
    def BY_SEQ(a: Transaction, b: Transaction) -> int:  # noqa: N802
        return compare(a.seq, b.seq)

    def __str__(self) -> str:
        return f"#{self.seq} {self.account} {self.amount}"


BY_AMOUNT: Comparator = Transaction.BY_AMOUNT
BY_SEQ: Comparator = Transaction.BY_SEQ


class CountingComparator:
    """Wraps a comparator and counts how many times it is consulted."""

    def __init__(self, inner: Comparator) -> None:
        self._inner = inner
        self.comparisons = 0

    def __call__(self, a: Any, b: Any) -> int:
        self.comparisons += 1
        return self._inner(a, b)

    @staticmethod
    def natural() -> CountingComparator:
        return CountingComparator(natural_order)


class Sorts:
    """Four comparison sorts over a list. All sort in place except merge_sort, which uses an O(n) buffer."""

    INSERTION_CUTOFF = 16

    @staticmethod
    def insertion_sort(a: list, cmp: Comparator, from_: int = 0, to: int | None = None) -> None:
        """O(n^2) worst and average, O(n) on nearly-sorted input, stable, in place."""
        if to is None:
            to = len(a)
        for i in range(from_ + 1, to):
            key = a[i]
            j = i - 1
            # strict > keeps equal keys in their original order: this is what makes it stable
            while j >= from_ and cmp(a[j], key) > 0:
                a[j + 1] = a[j]
                j -= 1
            a[j + 1] = key

    @staticmethod
    def merge_sort(a: list, cmp: Comparator) -> None:
        """O(n log n) always, stable, O(n) extra memory. Top-down with an insertion-sort base case."""
        buffer = [None] * len(a)
        Sorts._merge_sort(a, buffer, 0, len(a), cmp)

    @staticmethod
    def _merge_sort(a: list, buffer: list, from_: int, to: int, cmp: Comparator) -> None:
        if to - from_ <= Sorts.INSERTION_CUTOFF:
            Sorts.insertion_sort(a, cmp, from_, to)
            return
        mid = from_ + (to - from_) // 2
        Sorts._merge_sort(a, buffer, from_, mid, cmp)
        Sorts._merge_sort(a, buffer, mid, to, cmp)
        if cmp(a[mid - 1], a[mid]) <= 0:
            return  # halves already in order: skip the merge
        Sorts.merge(a, buffer, from_, mid, to, cmp)

    @staticmethod
    def merge(a: list, buffer: list, from_: int, mid: int, to: int, cmp: Comparator) -> None:
        buffer[from_:to] = a[from_:to]
        i, j, k = from_, mid, from_
        while i < mid and j < to:
            # <= takes from the left half on ties, so the earlier element stays earlier: stability
            if cmp(buffer[i], buffer[j]) <= 0:
                a[k] = buffer[i]
                i += 1
            else:
                a[k] = buffer[j]
                j += 1
            k += 1
        while i < mid:
            a[k] = buffer[i]
            i += 1
            k += 1
        while j < to:
            a[k] = buffer[j]
            j += 1
            k += 1

    @staticmethod
    def quick_sort(a: list, cmp: Comparator, rng: JavaRandom) -> None:
        """O(n log n) expected, not stable, three-way partition, random pivot."""
        Sorts._quick_sort(a, 0, len(a) - 1, cmp, rng)

    @staticmethod
    def _quick_sort(a: list, lo: int, hi: int, cmp: Comparator, rng: JavaRandom) -> None:
        while hi - lo + 1 > Sorts.INSERTION_CUTOFF:
            Sorts.swap(a, lo, lo + rng.next_int(hi - lo + 1))  # random pivot: no fixed input is a worst case
            pivot = a[lo]
            lt, gt, i = lo, hi, lo + 1
            while i <= gt:
                c = cmp(a[i], pivot)
                if c < 0:
                    Sorts.swap(a, lt, i)
                    lt += 1
                    i += 1
                elif c > 0:
                    Sorts.swap(a, i, gt)
                    gt -= 1
                else:
                    i += 1
            # a[lo..lt-1] < pivot, a[lt..gt] == pivot, a[gt+1..hi] > pivot
            # recurse on the smaller side, loop on the larger: stack depth stays O(log n)
            if lt - lo < hi - gt:
                Sorts._quick_sort(a, lo, lt - 1, cmp, rng)
                lo = gt + 1
            else:
                Sorts._quick_sort(a, gt + 1, hi, cmp, rng)
                hi = lt - 1
        Sorts.insertion_sort(a, cmp, lo, hi + 1)

    @staticmethod
    def heap_sort(a: list, cmp: Comparator) -> None:
        """O(n log n) always, in place, O(1) extra memory, not stable."""
        n = len(a)
        for i in range(n // 2 - 1, -1, -1):
            Sorts._sift_down(a, i, n, cmp)  # build max-heap bottom-up: O(n)
        for end in range(n - 1, 0, -1):
            Sorts.swap(a, 0, end)  # largest to its final slot
            Sorts._sift_down(a, 0, end, cmp)  # restore the heap on the prefix

    @staticmethod
    def _sift_down(a: list, root: int, size: int, cmp: Comparator) -> None:
        while True:
            left = 2 * root + 1
            right = left + 1
            largest = root
            if left < size and cmp(a[left], a[largest]) > 0:
                largest = left
            if right < size and cmp(a[right], a[largest]) > 0:
                largest = right
            if largest == root:
                return
            Sorts.swap(a, root, largest)
            root = largest

    @staticmethod
    def swap(a: list, i: int, j: int) -> None:
        a[i], a[j] = a[j], a[i]

    @staticmethod
    def is_sorted(a: list, cmp: Comparator) -> bool:
        """True when a[i] <= a[i+1] for every i."""
        return all(cmp(a[i - 1], a[i]) <= 0 for i in range(1, len(a)))

    @staticmethod
    def is_stable_permutation(original: list, sorted_: list, key: Comparator, identity: Comparator) -> bool:
        """True when every run of equal keys in `sorted_` appears in the same relative order as in `original`."""
        if len(original) != len(sorted_) or not Sorts.is_sorted(sorted_, key):
            return False
        remaining = list(original)
        for i in range(1, len(sorted_)):
            if key(sorted_[i - 1], sorted_[i]) == 0:
                p = Sorts._index_of(remaining, sorted_[i - 1], identity)
                q = Sorts._index_of(remaining, sorted_[i], identity)
                if p < 0 or q < 0 or p > q:
                    return False
        return True

    @staticmethod
    def _index_of(xs: list, x: Any, identity: Comparator) -> int:
        for i, y in enumerate(xs):
            if identity(y, x) == 0:
                return i
        return -1


def sample_ledger() -> list[Transaction]:
    usd = USD
    return [
        Transaction(1, "acct-A", Money.of_minor(1999, usd)),
        Transaction(2, "acct-B", Money.of_minor(500, usd)),
        Transaction(3, "acct-C", Money.of_minor(1999, usd)),
        Transaction(4, "acct-A", Money.of_minor(500, usd)),
        Transaction(5, "acct-D", Money.of_minor(12000, usd)),
        Transaction(6, "acct-B", Money.of_minor(1999, usd)),
        Transaction(7, "acct-E", Money.of_minor(500, usd)),
        Transaction(8, "acct-C", Money.of_minor(75, usd)),
    ]


def random_ints(n: int, bound: int, rng: JavaRandom) -> list[int]:
    return [rng.next_int(bound) for _ in range(n)]


def _b(x: bool) -> str:
    return "true" if x else "false"


def main() -> None:
    print("== Stable vs unstable: sort a ledger by amount, keep arrival order inside each amount ==")
    original = sample_ledger()
    stable = list(original)
    Sorts.merge_sort(stable, BY_AMOUNT)
    print("mergeSort (stable):")
    for t in stable:
        print(f"  {t}")
    unstable = list(original)
    Sorts.heap_sort(unstable, BY_AMOUNT)
    print("heapSort (not stable):")
    for t in unstable:
        print(f"  {t}")
    print("merge preserves seq order within equal amounts: "
          + _b(Sorts.is_stable_permutation(original, stable, BY_AMOUNT, BY_SEQ)))
    print("heap preserves seq order within equal amounts:  "
          + _b(Sorts.is_stable_permutation(original, unstable, BY_AMOUNT, BY_SEQ)))
    fixed = list(original)
    Sorts.heap_sort(fixed, then_comparing(BY_AMOUNT, BY_SEQ))
    print("heapSort with a (amount, seq) tie-break is deterministic: "
          + _b(Sorts.is_stable_permutation(original, fixed, BY_AMOUNT, BY_SEQ)))

    print()
    print("== Comparison counts, n = 4096 random ints, seed 42 ==")
    rng = JavaRandom(42)
    data = random_ints(4096, 1_000_000, rng)
    for name in ("insertion", "merge", "quick(3-way)", "heap"):
        copy = list(data)
        cc = CountingComparator.natural()
        if name == "insertion":
            Sorts.insertion_sort(copy, cc)
        elif name == "merge":
            Sorts.merge_sort(copy, cc)
        elif name == "quick(3-way)":
            Sorts.quick_sort(copy, cc, JavaRandom(7))
        else:
            Sorts.heap_sort(copy, cc)
        count = cc.comparisons
        print(f"  {name:<13} {count:>10,} comparisons  sorted={_b(Sorts.is_sorted(copy, cc))}")
    print(f"  n log2 n       {round(4096 * (math.log(4096) / math.log(2))):>10,}")

    print()
    print("== Duplicate-heavy input: 4096 values drawn from only 3 keys ==")
    dupes = random_ints(4096, 3, JavaRandom(1))
    q3 = CountingComparator.natural()
    Sorts.quick_sort(list(dupes), q3, JavaRandom(7))
    mg = CountingComparator.natural()
    Sorts.merge_sort(list(dupes), mg)
    print(f"  quick(3-way)  {q3.comparisons:>8,} comparisons (three-way partition finishes equal keys in one pass)")
    print(f"  merge         {mg.comparisons:>8,} comparisons")

    print()
    print("== Nearly sorted input: 4096 ascending ints with 8 adjacent swaps ==")
    nearly = list(range(4096))
    swapper = JavaRandom(3)
    for _ in range(8):  # 8 adjacent swaps
        i = swapper.next_int(4095)
        Sorts.swap(nearly, i, i + 1)
    ins = CountingComparator.natural()
    Sorts.insertion_sort(list(nearly), ins)
    hp = CountingComparator.natural()
    Sorts.heap_sort(list(nearly), hp)
    print(f"  insertion     {ins.comparisons:>8,} comparisons (adaptive: close to n)")
    print(f"  heap          {hp.comparisons:>8,} comparisons (oblivious: always n log n)")
    print()
    print("Library rule of thumb: sorted()/list.sort() are Timsort (stable, adaptive, O(n) buffer);")
    print("Python has no primitive arrays, so every sort is the stable object sort; key= replaces comparators.")


if __name__ == "__main__":
    main()
