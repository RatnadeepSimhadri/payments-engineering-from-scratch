"""Lesson: phases/03-systems-data-structures/07-skip-lists/docs/en.md
Skip Lists
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Pugh (1990), "Skip Lists: A Probabilistic Alternative to Balanced Trees", CACM 33(6);
Redis t_zset.c (zslInsert, zslDeleteNode, zslGetRank); java.util.concurrent.ConcurrentSkipListMap.
"""

from __future__ import annotations

import math
from decimal import ROUND_HALF_UP, Decimal
from typing import Any, Callable, Generic, Optional, TypeVar

K = TypeVar("K")
V = TypeVar("V")


def fmt_fixed(x: float, digits: int) -> str:
    """Java's %.Nf: round the shortest decimal repr HALF_UP (Python's f-format rounds the binary value)."""
    q = Decimal(repr(x)).quantize(Decimal(1).scaleb(-digits), rounding=ROUND_HALF_UP)
    return f"{q:.{digits}f}"


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

    def next_double(self) -> float:
        return ((self._next(26) << 27) + self._next(27)) * (1.0 / (1 << 53))


def java_shuffle(xs: list, rng: JavaRandom) -> None:
    """Collections.shuffle(list, rnd) for a RandomAccess list: swap(i - 1, nextInt(i)) from the end."""
    for i in range(len(xs), 1, -1):
        j = rng.next_int(i)
        xs[i - 1], xs[j] = xs[j], xs[i - 1]


class IllegalStateError(Exception):
    """Java's IllegalStateException: the invariant checker found a corrupted list."""


class Node:
    __slots__ = ("key", "value", "forward", "span")

    def __init__(self, key: Any, value: Any, levels: int) -> None:
        self.key = key
        self.value = value
        self.forward: list[Optional[Node]] = [None] * levels  # forward[i]: next node at level i
        self.span: list[int] = [0] * levels                   # span[i]: level-0 steps forward[i] jumps over


class SkipList(Generic[K, V]):
    """Skip list with Redis-style spans so rank queries are O(log n). Levels come from an injected RNG."""

    Node = Node

    def __init__(self, max_level: int, p: float, rng: JavaRandom) -> None:
        if max_level < 1 or max_level > 64:
            raise ValueError(f"maxLevel must be 1..64: {max_level}")
        if not 0 < p < 1:
            raise ValueError(f"p must be in (0,1): {p}")

        def coin() -> int:
            lvl = 1
            while lvl < max_level and rng.next_double() < p:
                lvl += 1
            return lvl

        self._init(max_level, p, coin)

    @classmethod
    def with_level_source(cls, max_level: int, p: float, level_source: Callable[[], int]) -> "SkipList":
        """For the degenerate-case demo: a list whose "coin" is supplied by the caller (Java's IntSupplier overload)."""
        self = cls.__new__(cls)
        self._init(max_level, p, level_source)
        return self

    def _init(self, max_level: int, p: float, level_source: Callable[[], int]) -> None:
        self.max_level = max_level
        self.p = p
        self._level_source = level_source  # returns a level in [1, max_level]
        self._header = Node(None, None, max_level)
        self._level = 1                     # levels currently in use
        self._size = 0
        self.search_steps = 0               # pointer advances across all searches

    @staticmethod
    def default_max_level(expected_size: int, p: float) -> int:
        return max(1, math.ceil(math.log(expected_size) / math.log(1.0 / p)))

    def random_level(self) -> int:
        return self._level_source()

    # ----- search -------------------------------------------------------------------------------

    def get(self, key: K) -> Optional[V]:
        """One step per forward move and one per level descended: the comparisons Pugh's analysis bounds."""
        x = self._header
        steps = 0
        for i in range(self._level - 1, -1, -1):
            nxt = x.forward[i]
            while nxt is not None and nxt.key < key:
                x = nxt
                nxt = x.forward[i]
                steps += 1
            steps += 1  # the comparison that failed, sending us down a level
        self.search_steps += steps
        x = x.forward[0]
        return x.value if x is not None and x.key == key else None

    def contains_key(self, key: K) -> bool:
        return self.get(key) is not None

    # ----- insert -------------------------------------------------------------------------------

    def put(self, key: K, value: V) -> None:
        update: list[Optional[Node]] = [None] * self.max_level
        rank = [0] * self.max_level
        x = self._header
        for i in range(self._level - 1, -1, -1):
            rank[i] = 0 if i == self._level - 1 else rank[i + 1]
            while x.forward[i] is not None and x.forward[i].key < key:
                rank[i] += x.span[i]
                x = x.forward[i]
            update[i] = x
        existing = x.forward[0]
        if existing is not None and existing.key == key:
            existing.value = value
            return

        lvl = self.random_level()
        if lvl > self._level:
            for i in range(self._level, lvl):
                rank[i] = 0
                update[i] = self._header
                update[i].span[i] = self._size  # the header's new level spans the whole list so far
            self._level = lvl
        node = Node(key, value, lvl)
        for i in range(lvl):
            node.forward[i] = update[i].forward[i]
            update[i].forward[i] = node
            node.span[i] = update[i].span[i] - (rank[0] - rank[i])
            update[i].span[i] = (rank[0] - rank[i]) + 1
        for i in range(lvl, self._level):
            update[i].span[i] += 1  # levels above the new node now jump over one more
        self._size += 1

    # ----- delete -------------------------------------------------------------------------------

    def remove(self, key: K) -> bool:
        update: list[Optional[Node]] = [None] * self.max_level
        x = self._header
        for i in range(self._level - 1, -1, -1):
            while x.forward[i] is not None and x.forward[i].key < key:
                x = x.forward[i]
            update[i] = x
        x = x.forward[0]
        if x is None or x.key != key:
            return False
        for i in range(self._level):
            if update[i].forward[i] is x:
                update[i].span[i] += x.span[i] - 1
                update[i].forward[i] = x.forward[i]
            else:
                update[i].span[i] -= 1
        while self._level > 1 and self._header.forward[self._level - 1] is None:
            self._level -= 1
        self._size -= 1
        return True

    # ----- rank and range ---------------------------------------------------------------------------

    def rank(self, key: K) -> int:
        """1-based position of key in sorted order, or 0 if absent. O(log n) thanks to spans."""
        traversed = 0
        x = self._header
        for i in range(self._level - 1, -1, -1):
            while x.forward[i] is not None and x.forward[i].key <= key:
                traversed += x.span[i]
                x = x.forward[i]
            if x is not self._header and x.key == key:
                return traversed
        return 0

    def by_rank(self, r: int) -> Optional[K]:
        """The key at 1-based rank r, or None. O(log n)."""
        if r < 1 or r > self._size:
            return None
        traversed = 0
        x = self._header
        for i in range(self._level - 1, -1, -1):
            while x.forward[i] is not None and traversed + x.span[i] <= r:
                traversed += x.span[i]
                x = x.forward[i]
            if traversed == r:
                return x.key
        return None

    def range(self, lo: K, hi: K) -> list:
        """Keys in [lo, hi]: descend to the first key >= lo, then walk level 0. O(log n + k)."""
        out: list = []
        x = self._header
        for i in range(self._level - 1, -1, -1):
            while x.forward[i] is not None and x.forward[i].key < lo:
                x = x.forward[i]
        x = x.forward[0]
        while x is not None and x.key <= hi:
            out.append(x.key)
            x = x.forward[0]
        return out

    def keys(self) -> list:
        out: list = []
        x = self._header.forward[0]
        while x is not None:
            out.append(x.key)
            x = x.forward[0]
        return out

    # ----- shape --------------------------------------------------------------------------------

    def size(self) -> int:
        return self._size

    def level(self) -> int:
        return self._level

    def level_histogram(self) -> list[int]:
        """Index i holds the count of nodes with at least i+1 levels."""
        h = [0] * self._level
        for i in range(self._level):
            x = self._header.forward[i]
            while x is not None:
                h[i] += 1
                x = x.forward[i]
        return h

    @staticmethod
    def expected_steps(n: int, p: float) -> float:
        """Pugh's expected search cost: about log_(1/p)(n) / p comparisons."""
        return math.log(n) / math.log(1.0 / p) / p

    def check_invariants(self) -> bool:
        """Level 0 sorted and complete; spans equal the level-0 distance; nothing above the current level."""
        base = self.keys()
        if len(base) != self._size:
            raise IllegalStateError(f"level 0 holds {len(base)} keys, size says {self._size}")
        for i in range(1, len(base)):
            if base[i - 1] >= base[i]:
                raise IllegalStateError(f"level 0 not sorted at {base[i]}")
        for i in range(self._level):
            position = 0  # 1-based rank of x; header is 0
            x = self._header
            while x.forward[i] is not None:
                target = position + x.span[i]
                if target < 1 or target > self._size or base[target - 1] != x.forward[i].key:
                    where = "header" if x is self._header else x.key
                    raise IllegalStateError(f"span mismatch at level {i} after {where}")
                position = target
                x = x.forward[i]
            if i == 0 and position != self._size:
                raise IllegalStateError("level 0 spans do not reach the end")
        for i in range(self._level, self.max_level):
            if self._header.forward[i] is not None:
                raise IllegalStateError(f"node above current level {i}")
        return True


def java_list(xs: list) -> str:
    return "[" + ", ".join(str(x) for x in xs) + "]"


def main() -> None:
    rng = JavaRandom(2024)

    print("== Merchant risk scores as a sorted set (Redis ZADD / ZRANK / ZRANGE) ==")
    scores: SkipList[int, str] = SkipList(16, 0.5, rng)
    sample = [742, 118, 905, 367, 512, 288, 674, 59, 831, 456]
    for i, s in enumerate(sample):
        scores.put(s, f"merchant_{i}")
    print(f"sorted: {java_list(scores.keys())}")
    print(f"rank of 512 = {scores.rank(512)}, key at rank 3 = {scores.by_rank(3)}, "
          f"scores in [300, 700] = {java_list(scores.range(300, 700))}")
    print(f"levels in use: {scores.level()}, nodes per level: {java_list(scores.level_histogram())}")

    print()
    print("== 100,000 keys: the geometric level distribution ==")
    n = 100_000
    big: SkipList[int, int] = SkipList(SkipList.default_max_level(n, 0.5), 0.5, rng)
    keys = list(range(n))
    java_shuffle(keys, rng)
    for k in keys:
        big.put(k, k)
    big.check_invariants()
    hist = big.level_histogram()
    print(f"maxLevel={big.max_level} levels in use={big.level()}")
    for i in range(min(len(hist), 8)):
        print(f"level {i:2d}: {hist[i]:6d} nodes  (expected n/2^{i} = {fmt_fixed(n / 2.0 ** i, 0):>6})")

    print()
    print("== Search cost: measured pointer steps vs Pugh's bound ==")
    big.search_steps = 0
    for _ in range(10_000):
        big.get(rng.next_int(n))
    print(f"average comparisons per get = {fmt_fixed(big.search_steps / 10_000.0, 1)}, "
          f"Pugh's expectation log2(n)/p = {fmt_fixed(SkipList.expected_steps(n, 0.5), 1)}, "
          f"log2(n) = {fmt_fixed(math.log(n) / math.log(2), 1)}")

    print()
    print("== Sorted inserts do not hurt: the shape depends on coins, not on keys ==")
    sorted_list: SkipList[int, int] = SkipList(17, 0.5, JavaRandom(7))
    for i in range(n):
        sorted_list.put(i, i)
    sorted_list.search_steps = 0
    for i in range(10_000):
        sorted_list.get(i * 10)
    print(f"sorted input: levels={sorted_list.level()}, "
          f"average steps per get = {fmt_fixed(sorted_list.search_steps / 10_000.0, 1)}")

    print()
    print("== What the coin buys: a list whose coin always says 1 is a linked list ==")
    flat: SkipList[int, int] = SkipList.with_level_source(16, 0.5, lambda: 1)
    # Inserted in descending order so each put is O(1) at the head; Java inserts ascending, which costs
    # 2 * 10^8 pointer steps (fine on the JIT, a minute in CPython). With the coin fixed at 1 the list
    # depends only on the key set, so the structure and every measurement below are identical.
    for i in range(20_000 - 1, -1, -1):
        flat.put(i, i)
    flat.search_steps = 0
    for _ in range(100):
        flat.get(rng.next_int(20_000))
    print(f"level={flat.level()}, average steps per get = {fmt_fixed(flat.search_steps / 100.0, 0)} (n/2 = {10_000})")

    print()
    print("== Removes keep spans and ranks consistent ==")
    for k in range(0, n, 2):
        big.remove(k)
    big.check_invariants()
    print(f"after removing even keys: size={big.size()}, rank(99999)={big.rank(99_999)}, "
          f"byRank(1)={big.by_rank(1)}, byRank(50000)={big.by_rank(50_000)}")


if __name__ == "__main__":
    main()
