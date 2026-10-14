"""Lesson: phases/01-complexity-and-core-structures/08-binary-search-trees/docs/en.md
Binary Search Trees and Ordered Maps.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Cormen, Leiserson, Rivest, Stein, "Introduction to Algorithms", ch. 12 (binary search trees);
Knuth, TAOCP vol. 3, 6.2.2. ISO 4217 for the Money type reused from Phase 10, Lesson 01. Stdlib only.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass
from typing import Generic, Optional, TypeVar

LONG_MIN = -(1 << 63)
LONG_MAX = (1 << 63) - 1


def add_exact(a: int, b: int) -> int:
    """Math.addExact for a Java long: Python ints never overflow, so the 64-bit range is checked by hand."""
    result = a + b
    if result < LONG_MIN or result > LONG_MAX:
        raise OverflowError("long overflow")
    return result


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code plus the number of decimal places in its minor unit (Phase 10, Lesson 01)."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code: {self.code}")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError("exponent must be 0..4")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)
Currency.USD = USD  # type: ignore[attr-defined]


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float. Trimmed copy of the Phase 10 type."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if self.currency is None:
            raise ValueError("currency")
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> Money:
        return Money(minor_units, currency)

    def plus(self, other: Money) -> Money:
        if self.currency != other.currency:
            raise ValueError("currency mismatch")
        return Money(add_exact(self.minor_units, other.minor_units), self.currency)

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        whole, frac = divmod(abs(self.minor_units), scale)  # on abs(): no floor-vs-truncate difference
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


@dataclass(frozen=True, order=True)
class TxKey:
    """Composite key: order by timestamp, then by sequence so two postings in the same millisecond stay distinct.
    order=True compares (timestamp_millis, sequence) as a tuple, which is exactly Long.compare then Long.compare."""

    timestamp_millis: int
    sequence: int

    @staticmethod
    def lowest(timestamp_millis: int) -> TxKey:
        return TxKey(timestamp_millis, LONG_MIN)

    @staticmethod
    def highest(timestamp_millis: int) -> TxKey:
        return TxKey(timestamp_millis, LONG_MAX)


@dataclass(frozen=True)
class Transaction:
    """A posting in the index: the key it is filed under plus what was paid."""

    id: str
    key: TxKey
    amount: Money


K = TypeVar("K")
V = TypeVar("V")


@dataclass(frozen=True)
class Entry(Generic[K, V]):
    """One key/value pair returned by traversals and range queries."""

    key: K
    value: V


class BinarySearchTree(Generic[K, V]):
    """Unbalanced binary search tree used as an ordered map.
    Invariant: for every node n, every key in n.left < n.key < every key in n.right. Keys are unique.
    Every operation is O(h). All traversals are iterative: Python's default recursion limit is 1,000."""

    class Node:
        __slots__ = ("key", "value", "left", "right")

        def __init__(self, key, value) -> None:
            self.key = key
            self.value = value
            self.left: Optional[BinarySearchTree.Node] = None
            self.right: Optional[BinarySearchTree.Node] = None

    def __init__(self) -> None:
        self._root: Optional[BinarySearchTree.Node] = None
        self._size = 0
        self._comparisons = 0  # instrumentation: how many key comparisons the tree has done

    def size(self) -> int:
        return self._size

    def is_empty(self) -> bool:
        return self._size == 0

    def comparisons(self) -> int:
        return self._comparisons

    def reset_comparisons(self) -> None:
        self._comparisons = 0

    def _compare(self, a: K, b: K) -> int:
        """compareTo as a function returning -1/0/1, counted once per call like the Java version."""
        self._comparisons += 1
        if a < b:
            return -1
        if b < a:
            return 1
        return 0

    def put(self, key: K, value: V) -> Optional[V]:
        """Insert or replace. Returns the previous value or None. Iterative so a chain cannot hit the recursion limit."""
        if key is None:
            raise TypeError("key")  # Java: NullPointerException from Objects.requireNonNull
        if self._root is None:
            self._root = BinarySearchTree.Node(key, value)
            self._size = 1
            return None
        cur = self._root
        while True:
            c = self._compare(key, cur.key)
            if c == 0:
                old = cur.value
                cur.value = value
                return old
            if c < 0:
                if cur.left is None:
                    cur.left = BinarySearchTree.Node(key, value)
                    self._size += 1
                    return None
                cur = cur.left
            else:
                if cur.right is None:
                    cur.right = BinarySearchTree.Node(key, value)
                    self._size += 1
                    return None
                cur = cur.right

    def get(self, key: K) -> Optional[V]:
        """Exact lookup: follow one path from the root. O(h)."""
        cur = self._root
        while cur is not None:
            c = self._compare(key, cur.key)
            if c == 0:
                return cur.value
            cur = cur.left if c < 0 else cur.right
        return None

    def contains_key(self, key: K) -> bool:
        return self.get(key) is not None

    def min_key(self) -> Optional[K]:
        if self._root is None:
            return None
        n = self._root
        while n.left is not None:
            n = n.left
        return n.key

    def max_key(self) -> Optional[K]:
        if self._root is None:
            return None
        n = self._root
        while n.right is not None:
            n = n.right
        return n.key

    def floor_key(self, key: K) -> Optional[K]:
        """Largest key less than or equal to the argument, or None."""
        cur = self._root
        best = None
        while cur is not None:
            c = self._compare(key, cur.key)
            if c == 0:
                return cur.key
            if c < 0:
                cur = cur.left
            else:
                best = cur.key
                cur = cur.right
        return best

    def ceiling_key(self, key: K) -> Optional[K]:
        """Smallest key greater than or equal to the argument, or None."""
        cur = self._root
        best = None
        while cur is not None:
            c = self._compare(key, cur.key)
            if c == 0:
                return cur.key
            if c > 0:
                cur = cur.right
            else:
                best = cur.key
                cur = cur.left
        return best

    def remove(self, key: K) -> Optional[V]:
        """Delete by key. 0/1 child: splice the child into the parent's slot. 2 children: copy the in-order
        successor into this node, then delete the successor node, which has no left child."""
        parent, cur = None, self._root
        while cur is not None:
            c = self._compare(key, cur.key)
            if c == 0:
                break
            parent = cur
            cur = cur.left if c < 0 else cur.right
        if cur is None:
            return None
        removed = cur.value

        if cur.left is not None and cur.right is not None:
            succ_parent, succ = cur, cur.right
            while succ.left is not None:
                succ_parent, succ = succ, succ.left
            cur.key = succ.key
            cur.value = succ.value
            if succ_parent is cur:
                succ_parent.right = succ.right
            else:
                succ_parent.left = succ.right
        else:
            child = cur.left if cur.left is not None else cur.right
            if parent is None:
                self._root = child
            elif parent.left is cur:
                parent.left = child
            else:
                parent.right = child
        self._size -= 1
        return removed

    def in_order(self) -> list[Entry[K, V]]:
        """All entries in ascending key order. Iterative in-order traversal with an explicit stack. O(n)."""
        out: list[Entry[K, V]] = []
        stack: list[BinarySearchTree.Node] = []
        cur = self._root
        while cur is not None or stack:
            while cur is not None:
                stack.append(cur)
                cur = cur.left
            cur = stack.pop()
            out.append(Entry(cur.key, cur.value))
            cur = cur.right
        return out

    def range_query(self, lo: K, hi: K) -> list[Entry[K, V]]:
        """All entries with lo <= key <= hi, ascending. O(h + k): subtrees that cannot match are never entered."""
        if self._compare(lo, hi) > 0:
            raise ValueError("lo must not exceed hi")
        out: list[Entry[K, V]] = []
        stack: list[BinarySearchTree.Node] = []
        cur = self._root
        while cur is not None or stack:
            while cur is not None:
                stack.append(cur)
                # prune: nothing in cur.left can be >= lo if cur.key < lo
                cur = None if self._compare(cur.key, lo) < 0 else cur.left
            cur = stack.pop()
            vs_lo = self._compare(cur.key, lo)
            vs_hi = self._compare(cur.key, hi)
            if vs_hi > 0:
                return out  # every later in-order key is larger still
            if vs_lo >= 0:
                out.append(Entry(cur.key, cur.value))
            cur = cur.right
        return out

    def height(self) -> int:
        """Number of edges on the longest root-to-leaf path; -1 for an empty tree. Iterative level-order walk."""
        if self._root is None:
            return -1
        h = -1
        level = deque([self._root])
        while level:
            h += 1
            for _ in range(len(level)):
                n = level.popleft()
                if n.left is not None:
                    level.append(n.left)
                if n.right is not None:
                    level.append(n.right)
        return h

    def check_invariant(self) -> bool:
        """Verifies the ordering invariant and the size counter."""
        all_entries = self.in_order()
        if len(all_entries) != self._size:
            return False
        for i in range(1, len(all_entries)):
            if not all_entries[i - 1].key < all_entries[i].key:
                return False
        return True


class TransactionIndex:
    """Index of transactions by (timestamp, sequence) so "everything between t1 and t2" is one range query."""

    def __init__(self) -> None:
        self._tree: BinarySearchTree[TxKey, Transaction] = BinarySearchTree()
        self._next_sequence = 0

    def record(self, id: str, timestamp_millis: int, amount: Money) -> Transaction:
        tx = Transaction(id, TxKey(timestamp_millis, self._next_sequence), amount)
        self._next_sequence += 1
        self._tree.put(tx.key, tx)
        return tx

    def remove(self, tx: Transaction) -> bool:
        return self._tree.remove(tx.key) is not None

    def between(self, from_inclusive: int, to_inclusive: int) -> list[Transaction]:
        """Every transaction with from_inclusive <= timestamp <= to_inclusive, oldest first."""
        entries = self._tree.range_query(TxKey.lowest(from_inclusive), TxKey.highest(to_inclusive))
        return [e.value for e in entries]

    def sum_between(self, from_inclusive: int, to_inclusive: int, currency: Currency) -> Money:
        total = Money.of_minor(0, currency)
        for tx in self.between(from_inclusive, to_inclusive):
            total = total.plus(tx.amount)
        return total

    def size(self) -> int:
        return self._tree.size()

    def height(self) -> int:
        return self._tree.height()

    def tree(self) -> BinarySearchTree[TxKey, Transaction]:
        return self._tree


class JavaRandom:
    """java.util.Random, bit for bit (48-bit LCG), so the demo amounts and shuffle match the Java output."""

    _MULTIPLIER = 0x5DEECE66D
    _ADDEND = 0xB
    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULTIPLIER) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULTIPLIER + self._ADDEND) & self._MASK
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        r = u % bound
        while u - r + m >= (1 << 31):  # Java's int overflow test: u - r + m < 0
            u = self.next(31)
            r = u % bound
        return r


T0 = 1_700_000_000_000  # 2023-11-14T22:13:20Z, a fixed epoch for determinism
MINUTE = 60_000


def _keys_str(t: BinarySearchTree) -> str:
    return "[" + ", ".join(str(e.key) for e in t.in_order()) + "]"


def demo_ordered_map() -> None:
    print("== Insert, search, delete with successor ==")
    t: BinarySearchTree[int, str] = BinarySearchTree()
    for k in (50, 30, 70, 20, 40, 60, 80, 35, 45):
        t.put(k, f"v{k}")
    print(f"in-order keys: {_keys_str(t)}")
    print(f"size={t.size()} height={t.height()} min={t.min_key()} max={t.max_key()}")
    print(f"floor(42)={t.floor_key(42)} ceiling(42)={t.ceiling_key(42)} get(45)={t.get(45)}")
    t.remove(30)  # two children: successor is 35
    print(f"after remove(30), in-order: {_keys_str(t)} invariant={'true' if t.check_invariant() else 'false'}")
    t.remove(50)  # root with two children: successor is 60
    print(f"after remove(50), in-order: {_keys_str(t)} height={t.height()}")
    print()


def demo_range_query() -> None:
    print("== Range query over transactions by timestamp ==")
    idx = TransactionIndex()
    rnd = JavaRandom(42)
    # 12 transactions spread over 90 minutes, inserted in arrival order (not timestamp order)
    offsets = [5, 71, 12, 44, 44, 88, 3, 60, 29, 15, 52, 44]
    for i, offset in enumerate(offsets):
        cents = 100 + rnd.next_int(9_900)
        idx.record(f"tx-{i + 1}", T0 + offset * MINUTE, Money.of_minor(cents, USD))
    print(f"indexed {idx.size()} transactions, tree height {idx.height()}")
    frm, to = T0 + 10 * MINUTE, T0 + 45 * MINUTE
    window = idx.between(frm, to)
    print(f"between +10min and +45min (inclusive): {len(window)} transactions")
    for tx in window:
        print("  %-6s +%2d min  %s" % (tx.id, (tx.key.timestamp_millis - T0) // MINUTE, tx.amount))
    print(f"sum in window = {idx.sum_between(frm, to, USD)}")
    print("three transactions share +44 min and all three are kept (composite key)")
    print()


def demo_degenerate() -> None:
    print("== Why unbalanced trees need balancing ==")
    n = 4_096
    sorted_tree: BinarySearchTree[int, int] = BinarySearchTree()
    for i in range(n):
        sorted_tree.put(i, i)
    sorted_tree.reset_comparisons()
    for i in range(n):
        sorted_tree.get(i)
    sorted_cmp = sorted_tree.comparisons()

    shuffled: BinarySearchTree[int, int] = BinarySearchTree()
    keys = list(range(n))
    rnd = JavaRandom(7)
    for i in range(n - 1, 0, -1):
        j = rnd.next_int(i + 1)
        keys[i], keys[j] = keys[j], keys[i]
    for k in keys:
        shuffled.put(k, k)
    shuffled.reset_comparisons()
    for i in range(n):
        shuffled.get(i)
    shuffled_cmp = shuffled.comparisons()

    print("%d keys inserted in sorted order:   height %d, %.1f comparisons per lookup"
          % (n, sorted_tree.height(), sorted_cmp / n))
    print("%d keys inserted in shuffled order: height %d, %.1f comparisons per lookup"
          % (n, shuffled.height(), shuffled_cmp / n))
    print("ideal balanced height for %d keys: %d" % (n, n.bit_length() - 1))
    print("transactions arrive in timestamp order, which is the worst case; Phase 3 fixes this with self-balancing trees")


def main() -> None:
    demo_ordered_map()
    demo_range_query()
    demo_degenerate()


if __name__ == "__main__":
    main()
