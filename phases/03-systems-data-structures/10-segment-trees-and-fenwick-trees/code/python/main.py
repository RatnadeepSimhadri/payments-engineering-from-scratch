"""Lesson: phases/03-systems-data-structures/10-segment-trees-and-fenwick-trees/docs/en.md
Segment Trees and Fenwick Trees
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: P. Fenwick, "A New Data Structure for Cumulative Frequency Tables" (SP&E, 1994);
de Berg et al., "Computational Geometry", ch. 10 (segment trees). Python 3.11+, standard library only.
"""

from __future__ import annotations

from dataclasses import dataclass

LONG_MIN, LONG_MAX = -(1 << 63), (1 << 63) - 1


# ----- Java long semantics: Python ints never overflow, so the Math.*Exact checks are explicit ---------------

def add_exact(a: int, b: int) -> int:
    """Math.addExact: a long sum outside [-2^63, 2^63) is an error, never a silent wrap."""
    r = a + b
    if not LONG_MIN <= r <= LONG_MAX:
        raise OverflowError("long overflow")
    return r


def subtract_exact(a: int, b: int) -> int:
    r = a - b
    if not LONG_MIN <= r <= LONG_MAX:
        raise OverflowError("long overflow")
    return r


def multiply_exact(a: int, b: int) -> int:
    r = a * b
    if not LONG_MIN <= r <= LONG_MAX:
        raise OverflowError("long overflow")
    return r


def java_div(a: int, b: int) -> int:
    """Java's / on longs truncates toward zero; Python's // floors."""
    q = abs(a) // abs(b)
    return -q if (a < 0) != (b < 0) else q


def java_rem(a: int, b: int) -> int:
    """Java's % takes the sign of the dividend."""
    r = abs(a) % abs(b)
    return -r if a < 0 else r


def check_index(index: int, length: int) -> int:
    """Objects.checkIndex: Python lists would accept a negative index, so reject it explicitly."""
    if not 0 <= index < length:
        raise IndexError(f"Index {index} out of bounds for length {length}")
    return index


class JavaRandom:
    """java.util.Random, bit for bit: 48-bit LCG, so the same seed gives the same stream as Java."""

    _MULT = 0x5DEECE66D
    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULT) & self._MASK

    def _next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULT + 0xB) & self._MASK
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r  # (int) cast

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self._next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        while True:
            r = u % bound
            if (u - r + m) & 0xFFFFFFFF < 0x80000000:  # Java: u - r + m >= 0 in 32-bit int
                return r
            u = self._next(31)

    def next_boolean(self) -> bool:
        return self._next(1) != 0


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError("currency code must be three letters")
        if not 0 <= self.exponent <= 4:
            raise ValueError("exponent must be 0..4")


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. The trees hold raw minor units; Money is the display type."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if self.currency is None:
            raise TypeError("currency")
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        sign = "-" if self.minor_units < 0 else ""
        whole, frac = divmod(abs(self.minor_units), scale)
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


class FenwickTree:
    """Binary indexed tree: point add and prefix sum in O(log n). 1-indexed inside, 0-indexed API."""

    def __init__(self, n: int) -> None:
        if n <= 0:
            raise ValueError(f"size must be positive: {n}")
        self._n = n
        self._tree = [0] * (n + 1)  # tree[0] unused

    @staticmethod
    def build(values: list[int]) -> "FenwickTree":
        """O(n) construction: each node pushes its total to its parent once."""
        f = FenwickTree(len(values))
        f._tree[1:] = values
        for i in range(1, f._n + 1):
            parent = i + (i & -i)
            if parent <= f._n:
                f._tree[parent] = add_exact(f._tree[parent], f._tree[i])
        return f

    def size(self) -> int:
        return self._n

    def add(self, index: int, delta: int) -> None:
        """values[index] += delta. Walks up: i += lowest set bit of i."""
        check_index(index, self._n)
        i = index + 1
        while i <= self._n:
            self._tree[i] = add_exact(self._tree[i], delta)
            i += i & -i

    def prefix_sum(self, index: int) -> int:
        """Sum of values[0..index] inclusive. Walks down: i -= lowest set bit. Returns 0 for index -1."""
        if index < -1 or index >= self._n:
            raise IndexError(f"index {index} for size {self._n}")
        s = 0
        i = index + 1
        while i > 0:
            s = add_exact(s, self._tree[i])
            i -= i & -i
        return s

    def range_sum(self, lo: int, hi: int) -> int:
        if lo > hi:
            raise ValueError(f"lo > hi: {lo} > {hi}")
        check_index(lo, self._n)
        check_index(hi, self._n)
        return subtract_exact(self.prefix_sum(hi), self.prefix_sum(lo - 1))

    def get(self, index: int) -> int:
        return self.range_sum(index, index)

    def first_index_with_prefix_at_least(self, target: int) -> int:
        """Smallest index whose prefix sum >= target, or -1. Requires values >= 0. Binary descent, O(log n)."""
        if target <= 0:
            return 0
        pos = 0
        remaining = target
        step = 1 << (self._n.bit_length() - 1)  # Integer.highestOneBit(n)
        while step > 0:
            nxt = pos + step
            if nxt <= self._n and self._tree[nxt] < remaining:
                pos = nxt
                remaining -= self._tree[nxt]
            step >>= 1
        return pos if pos < self._n else -1


class SegmentTree:
    """Point set and range sum, array-backed heap layout; leaves start at `size` (next power of two)."""

    def __init__(self, values: list[int]) -> None:
        if not values:
            raise ValueError("size must be positive")
        self._n = len(values)
        s = 1
        while s < self._n:
            s <<= 1
        self._size = s
        self._sum = [0] * (2 * s)
        self._sum[s:s + self._n] = values
        for k in range(s - 1, 0, -1):
            self._sum[k] = add_exact(self._sum[2 * k], self._sum[2 * k + 1])

    def size(self) -> int:
        return self._n

    def set(self, index: int, value: int) -> None:
        """values[index] = value; recompute the log n ancestors."""
        check_index(index, self._n)
        k = index + self._size
        self._sum[k] = value
        k >>= 1
        while k >= 1:
            self._sum[k] = add_exact(self._sum[2 * k], self._sum[2 * k + 1])
            k >>= 1

    def get(self, index: int) -> int:
        check_index(index, self._n)
        return self._sum[index + self._size]

    def range_sum(self, lo: int, hi: int) -> int:
        """Bottom-up: shrink both ends toward each other, adding whole nodes. Separate left/right accumulators."""
        if lo > hi:
            raise ValueError(f"lo > hi: {lo} > {hi}")
        check_index(lo, self._n)
        check_index(hi, self._n)
        left = right = 0
        l, r = lo + self._size, hi + self._size + 1  # half-open [l, r)
        while l < r:
            if l & 1:
                left = add_exact(left, self._sum[l])
                l += 1
            if r & 1:
                r -= 1
                right = add_exact(self._sum[r], right)
            l >>= 1
            r >>= 1
        return add_exact(left, right)


class LazySegmentTree:
    """Range add and range sum in O(log n). pending[k] applies to every value under k, not yet pushed down."""

    def __init__(self, values: list[int]) -> None:
        if not values:
            raise ValueError("size must be positive")
        self._n = len(values)
        self._sum = [0] * (4 * self._n)
        self._pending = [0] * (4 * self._n)
        self._build(1, 0, self._n - 1, values)

    def _build(self, k: int, lo: int, hi: int, values: list[int]) -> None:
        if lo == hi:
            self._sum[k] = values[lo]
            return
        mid = (lo + hi) >> 1
        self._build(2 * k, lo, mid, values)
        self._build(2 * k + 1, mid + 1, hi, values)
        self._sum[k] = add_exact(self._sum[2 * k], self._sum[2 * k + 1])

    def size(self) -> int:
        return self._n

    def _apply(self, k: int, count: int, delta: int) -> None:
        """Node k covers `count` values: its sum grows by count * delta."""
        self._sum[k] = add_exact(self._sum[k], multiply_exact(delta, count))
        self._pending[k] = add_exact(self._pending[k], delta)

    def _push(self, k: int, lo: int, hi: int) -> None:
        """Push k's pending addend one level down so its children are exact before we look at them."""
        if self._pending[k] == 0:
            return
        mid = (lo + hi) >> 1
        self._apply(2 * k, mid - lo + 1, self._pending[k])
        self._apply(2 * k + 1, hi - mid, self._pending[k])
        self._pending[k] = 0

    def range_add(self, lo: int, hi: int, delta: int) -> None:
        if lo > hi:
            raise ValueError(f"lo > hi: {lo} > {hi}")
        check_index(lo, self._n)
        check_index(hi, self._n)
        self._range_add(1, 0, self._n - 1, lo, hi, delta)

    def _range_add(self, k: int, node_lo: int, node_hi: int, lo: int, hi: int, delta: int) -> None:
        if hi < node_lo or node_hi < lo:
            return  # disjoint
        if lo <= node_lo and node_hi <= hi:
            self._apply(k, node_hi - node_lo + 1, delta)  # fully covered: stop here
            return
        self._push(k, node_lo, node_hi)
        mid = (node_lo + node_hi) >> 1
        self._range_add(2 * k, node_lo, mid, lo, hi, delta)
        self._range_add(2 * k + 1, mid + 1, node_hi, lo, hi, delta)
        self._sum[k] = add_exact(self._sum[2 * k], self._sum[2 * k + 1])

    def range_sum(self, lo: int, hi: int) -> int:
        if lo > hi:
            raise ValueError(f"lo > hi: {lo} > {hi}")
        check_index(lo, self._n)
        check_index(hi, self._n)
        return self._range_sum(1, 0, self._n - 1, lo, hi)

    def _range_sum(self, k: int, node_lo: int, node_hi: int, lo: int, hi: int) -> int:
        if hi < node_lo or node_hi < lo:
            return 0
        if lo <= node_lo and node_hi <= hi:
            return self._sum[k]
        self._push(k, node_lo, node_hi)
        mid = (node_lo + node_hi) >> 1
        return add_exact(self._range_sum(2 * k, node_lo, mid, lo, hi),
                         self._range_sum(2 * k + 1, mid + 1, node_hi, lo, hi))

    def get(self, index: int) -> int:
        return self.range_sum(index, index)


class BalanceHistory:
    """One account's closing balance per day, backed by a LazySegmentTree: a posting on day d shifts d..end."""

    def __init__(self, opening_balance_minor: int, days: int, currency: Currency) -> None:
        self._balances = LazySegmentTree([opening_balance_minor] * days)
        self._currency = currency

    def days(self) -> int:
        return self._balances.size()

    def post(self, day: int, delta_minor: int) -> None:
        self._balances.range_add(day, self._balances.size() - 1, delta_minor)

    def balance_on(self, day: int) -> Money:
        return Money.of_minor(self._balances.get(day), self._currency)

    def sum_of_balances(self, from_day: int, to_day: int) -> Money:
        return Money.of_minor(self._balances.range_sum(from_day, to_day), self._currency)

    def average_daily_balance(self, from_day: int, to_day: int) -> Money:
        """Average daily balance over [from_day, to_day], rounded half-even to a minor unit."""
        total = self._balances.range_sum(from_day, to_day)
        return Money.of_minor(BalanceHistory.divide_half_even(total, to_day - from_day + 1), self._currency)

    @staticmethod
    def divide_half_even(dividend: int, divisor: int) -> int:
        q, r = java_div(dividend, divisor), java_rem(dividend, divisor)
        if r == 0:
            return q
        sign = -1 if (dividend < 0) != (divisor < 0) else 1
        twice, d = abs(r) * 2, abs(divisor)
        if twice > d:
            return q + sign
        if twice < d:
            return q
        return q if java_rem(q, 2) == 0 else q + sign


def naive_range_sum(values: list[int], lo: int, hi: int) -> int:
    """Reference implementation: the thing the trees must agree with on every query."""
    return sum(values[lo:hi + 1])


def java_list(xs: list) -> str:
    return "[" + ", ".join(str(x) for x in xs) + "]"


def fenwick_internals(n: int) -> str:
    parts = []
    for i in range(1, n + 1):
        span = i & -i
        parts.append(f"t{i}=sum({i - span}..{i - 1})")
    return "[" + ", ".join(parts) + "]"


def main() -> None:
    print("== 1. Fenwick tree: daily net postings, balance by prefix sum ==")
    daily_net = [12_500, -3_200, 0, 4_150, -900, 7_000, -2_250, 1_000]  # minor units, 8 days
    fw = FenwickTree.build(daily_net)
    print(f"  daily net   = {java_list(daily_net)}")
    print(f"  balances    = {java_list([fw.prefix_sum(d) for d in range(len(daily_net))])}")
    print(f"  net days 2..5 = {Money.of_minor(fw.range_sum(2, 5), USD)}")
    print("  backdated correction: day 1 was one dollar more negative than recorded, add(1, -100)")
    fw.add(1, -100)
    print(f"  balance day 1 = {Money.of_minor(fw.prefix_sum(1), USD)}, day 7 = {Money.of_minor(fw.prefix_sum(7), USD)}"
          " (every later balance moved by one dollar)")
    print(f"  tree[] internals (1-indexed, node i covers i & -i values): {fenwick_internals(len(daily_net))}")

    print()
    print("== 2. Segment tree: same queries, point set instead of point add ==")
    st = SegmentTree(daily_net)
    print(f"  sum days 2..5 before = {st.range_sum(2, 5)}")
    st.set(3, 10_000)
    print(f"  set day 3 to 10000: sum days 2..5 after = {st.range_sum(2, 5)}, "
          f"naive = {naive_range_sum([12_500, -3_200, 0, 10_000, -900, 7_000, -2_250, 1_000], 2, 5)}")

    print()
    print("== 3. Lazy segment tree: backdated posting = range add over closing balances ==")
    history = BalanceHistory(100_000, 30, USD)  # 1000.00 USD opening, 30 days
    history.post(0, 12_500)    # +125.00 on day 0
    history.post(10, -40_000)  # -400.00 on day 10
    history.post(20, 5_000)    # +50.00 on day 20
    print(f"  balance day 0 = {history.balance_on(0)}, day 10 = {history.balance_on(10)}, "
          f"day 29 = {history.balance_on(29)}")
    print(f"  average daily balance days 0..29 = {history.average_daily_balance(0, 29)}")
    print("  a backdated posting arrives: -75.00 effective day 5")
    history.post(5, -7_500)
    print(f"  balance day 4 = {history.balance_on(4)}, day 5 = {history.balance_on(5)}, "
          f"day 29 = {history.balance_on(29)}")
    print(f"  average daily balance days 0..29 = {history.average_daily_balance(0, 29)}"
          " (25 of 30 days moved by -75.00: 25 * 75 / 30 = 62.50 lower)")
    print("  one rangeAdd touched O(log 30) nodes, not 25 days")

    print()
    print("== 4. Fenwick descent: which day does cumulative volume cross 20000 minor units? ==")
    volume = [5_000, 3_000, 8_000, 2_000, 6_000, 1_000]
    vol = FenwickTree.build(volume)
    print(f"  volume      = {java_list(volume)}")
    print(f"  first index with prefix >= 20000 = {vol.first_index_with_prefix_at_least(20_000)}"
          " (prefix sums 5000, 8000, 16000, 18000, 24000)")
    print(f"  first index with prefix >= 25001 = {vol.first_index_with_prefix_at_least(25_001)} (total is 25000)")

    print()
    print("== 5. Randomized agreement: 20000 mixed operations against the naive array ==")
    rng = JavaRandom(42)
    n = 257  # deliberately not a power of two
    model = [0] * n
    lazy = LazySegmentTree(model)
    fenwick = FenwickTree(n)
    mismatches = 0
    for _ in range(20_000):
        a, b = rng.next_int(n), rng.next_int(n)
        lo, hi = min(a, b), max(a, b)
        if rng.next_boolean():
            delta = rng.next_int(2001) - 1000
            for i in range(lo, hi + 1):
                model[i] += delta
                fenwick.add(i, delta)
            lazy.range_add(lo, hi, delta)
        else:
            expect = naive_range_sum(model, lo, hi)
            if lazy.range_sum(lo, hi) != expect or fenwick.range_sum(lo, hi) != expect:
                mismatches += 1
    print(f"  mismatches = {mismatches}")

    print()
    print("== 6. Cost: 10 million days, one query ==")
    big = 10_000_000
    bits = big.bit_length()  # 32 - Integer.numberOfLeadingZeros(big)
    print(f"  naive range sum: up to {big} additions; Fenwick: 2 * {bits} node reads; "
          f"segment tree: about 2 * {bits} node reads")
    print(f"  memory: Fenwick {big + 1} longs; segment tree {2 * (1 << (bits - 1)) * 2} longs; "
          f"lazy tree 2 x {4 * big} longs")


if __name__ == "__main__":
    main()
