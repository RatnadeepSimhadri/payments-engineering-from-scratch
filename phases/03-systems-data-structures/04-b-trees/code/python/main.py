"""Lesson: phases/03-systems-data-structures/04-b-trees/docs/en.md
B-Trees and Why Databases Love Them
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Bayer & McCreight (1972); Comer (1979), "The Ubiquitous B-Tree"; Knuth TAOCP Vol. 3 Sec. 6.2.4;
PostgreSQL src/backend/access/nbtree/README. Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from decimal import ROUND_HALF_UP, Decimal
from typing import Any, Generic, Optional, TypeVar

K = TypeVar("K")
V = TypeVar("V")

LONG_MAX = (1 << 63) - 1


def to_int64(x: int) -> int:
    """Reinterpret the low 64 bits as a signed Java long."""
    x &= 0xFFFFFFFFFFFFFFFF
    return x - (1 << 64) if x >= (1 << 63) else x


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

    def next_long(self) -> int:
        return to_int64((self._next(32) << 32) + self._next(32))


def java_shuffle(xs: list, rng: JavaRandom) -> None:
    """Collections.shuffle(list, rnd) for a RandomAccess list: swap(i - 1, nextInt(i)) from the end."""
    for i in range(len(xs), 1, -1):
        j = rng.next_int(i)
        xs[i - 1], xs[j] = xs[j], xs[i - 1]


class IllegalStateError(Exception):
    """Java's IllegalStateException: the invariant checker found a corrupted tree."""


class PageStats:
    """Counts page-level I/O. Every node is one page; a search touches one page per level."""

    def __init__(self) -> None:
        self.reads = self.writes = self.splits = self.merges = 0

    def reset(self) -> None:
        self.reads = self.writes = self.splits = self.merges = 0

    def __str__(self) -> str:
        return f"reads={self.reads} writes={self.writes} splits={self.splits} merges={self.merges}"


class BTreeNode:
    __slots__ = ("keys", "values", "children")

    def __init__(self) -> None:
        self.keys: list = []
        self.values: list = []
        self.children: list[BTreeNode] = []  # empty for a leaf

    def is_leaf(self) -> bool:
        return not self.children

    def key_count(self) -> int:
        return len(self.keys)


@dataclass(frozen=True)
class Split:
    """The median key promoted to the parent together with the new right sibling."""

    key: Any
    value: Any
    right: BTreeNode


class BTree(Generic[K, V]):
    """B-tree of a configurable order (max children). Nodes hold minKeys..maxKeys keys; leaves share one depth."""

    Node = BTreeNode

    def __init__(self, order: int) -> None:
        if order < 3:
            raise ValueError(f"order must be at least 3: {order}")
        self.order = order
        self.max_keys = order - 1
        self.min_keys = (order + 1) // 2 - 1   # ceil(order / 2) - 1: at least half full
        self.stats = PageStats()
        self._root = BTreeNode()
        self._size = 0

    # ----- search -------------------------------------------------------------------------------

    @staticmethod
    def lower_bound(n: BTreeNode, key: Any) -> int:
        """Index of the first key >= key in the node (binary search within the page)."""
        lo, hi = 0, len(n.keys)
        keys = n.keys
        while lo < hi:
            mid = (lo + hi) >> 1
            if keys[mid] < key:
                lo = mid + 1
            else:
                hi = mid
        return lo

    def get(self, key: K) -> Optional[V]:
        n = self._root
        while True:
            self.stats.reads += 1
            i = self.lower_bound(n, key)
            if i < len(n.keys) and n.keys[i] == key:
                return n.values[i]
            if n.is_leaf():
                return None
            n = n.children[i]

    def contains_key(self, key: K) -> bool:
        return self.get(key) is not None

    # ----- insert -------------------------------------------------------------------------------

    def put(self, key: K, value: V) -> None:
        split = self._insert(self._root, key, value)
        if split is not None:  # the root overflowed: the tree grows by one level
            new_root = BTreeNode()
            new_root.keys.append(split.key)
            new_root.values.append(split.value)
            new_root.children.append(self._root)
            new_root.children.append(split.right)
            self._root = new_root
            self.stats.writes += 1

    def _insert(self, n: BTreeNode, key: K, value: V) -> Optional[Split]:
        """Insert into the subtree at n; returns a Split if n overflowed and had to be divided."""
        self.stats.reads += 1
        i = self.lower_bound(n, key)
        if i < len(n.keys) and n.keys[i] == key:
            n.values[i] = value
            self.stats.writes += 1
            return None
        if n.is_leaf():
            n.keys.insert(i, key)
            n.values.insert(i, value)
            self._size += 1
        else:
            child_split = self._insert(n.children[i], key, value)
            if child_split is None:
                return None
            n.keys.insert(i, child_split.key)
            n.values.insert(i, child_split.value)
            n.children.insert(i + 1, child_split.right)
        self.stats.writes += 1
        return self._split(n) if len(n.keys) > self.max_keys else None

    def _split(self, n: BTreeNode) -> Split:
        """Divide an overflowing node at its median. Left keeps the lower half, right takes the upper half."""
        mid = len(n.keys) // 2
        mid_key, mid_value = n.keys[mid], n.values[mid]
        right = BTreeNode()
        right.keys = n.keys[mid + 1:]
        right.values = n.values[mid + 1:]
        del n.keys[mid:]
        del n.values[mid:]
        if not n.is_leaf():
            right.children = n.children[mid + 1:]
            del n.children[mid + 1:]
        self.stats.splits += 1
        self.stats.writes += 1  # the new right page
        return Split(mid_key, mid_value, right)

    # ----- delete -------------------------------------------------------------------------------

    def remove(self, key: K) -> None:
        self._remove(self._root, key)
        if len(self._root.keys) == 0 and not self._root.is_leaf():  # the root emptied: shrink by one level
            self._root = self._root.children[0]

    def _remove(self, n: BTreeNode, key: K) -> None:
        self.stats.reads += 1
        i = self.lower_bound(n, key)
        found = i < len(n.keys) and n.keys[i] == key
        if n.is_leaf():
            if not found:
                return
            del n.keys[i]
            del n.values[i]
            self._size -= 1
            self.stats.writes += 1
            return
        if found:
            # Replace with the in-order predecessor (largest key in the left child), then delete that from the child.
            pred = n.children[i]
            while not pred.is_leaf():
                self.stats.reads += 1
                pred = pred.children[-1]
            self.stats.reads += 1
            pred_key, pred_value = pred.keys[-1], pred.values[-1]
            n.keys[i] = pred_key
            n.values[i] = pred_value
            self.stats.writes += 1
            self._remove(n.children[i], pred_key)
        else:
            self._remove(n.children[i], key)
        if len(n.children[i].keys) < self.min_keys:
            self._rebalance(n, i)

    def _rebalance(self, parent: BTreeNode, i: int) -> None:
        """Child i of parent underflowed: borrow from a sibling that can spare a key, else merge with one."""
        child = parent.children[i]
        if i > 0 and len(parent.children[i - 1].keys) > self.min_keys:
            left = parent.children[i - 1]  # rotate one key through the parent, from the left
            child.keys.insert(0, parent.keys[i - 1])
            child.values.insert(0, parent.values[i - 1])
            parent.keys[i - 1] = left.keys.pop()
            parent.values[i - 1] = left.values.pop()
            if not left.is_leaf():
                child.children.insert(0, left.children.pop())
            self.stats.writes += 3
        elif i < len(parent.children) - 1 and len(parent.children[i + 1].keys) > self.min_keys:
            right = parent.children[i + 1]  # rotate one key through the parent, from the right
            child.keys.append(parent.keys[i])
            child.values.append(parent.values[i])
            parent.keys[i] = right.keys.pop(0)
            parent.values[i] = right.values.pop(0)
            if not right.is_leaf():
                child.children.append(right.children.pop(0))
            self.stats.writes += 3
        else:
            left_index = i - 1 if i > 0 else i  # merge child with a sibling around the separator
            left = parent.children[left_index]
            right = parent.children[left_index + 1]
            left.keys.append(parent.keys.pop(left_index))
            left.values.append(parent.values.pop(left_index))
            left.keys.extend(right.keys)
            left.values.extend(right.values)
            left.children.extend(right.children)
            del parent.children[left_index + 1]
            self.stats.merges += 1
            self.stats.writes += 2

    # ----- range scan and traversal ---------------------------------------------------------------

    def range(self, lo: K, hi: K) -> list:
        """Keys in [lo, hi] ascending. Descends only into children whose range can intersect."""
        out: list = []
        self._range(self._root, lo, hi, out)
        return out

    def _range(self, n: BTreeNode, lo: K, hi: K, out: list) -> None:
        self.stats.reads += 1
        i = self.lower_bound(n, lo)
        leaf = n.is_leaf()
        if not leaf:
            self._range(n.children[i], lo, hi, out)
        while i < len(n.keys):
            k = n.keys[i]
            if k > hi:
                return
            out.append(k)
            if not leaf:
                self._range(n.children[i + 1], lo, hi, out)
            i += 1

    def keys(self) -> list:
        out: list = []
        self._collect(self._root, out)
        return out

    def _collect(self, n: BTreeNode, out: list) -> None:
        if n.is_leaf():
            out.extend(n.keys)
            return
        for i, k in enumerate(n.keys):
            self._collect(n.children[i], out)
            out.append(k)
        self._collect(n.children[-1], out)

    # ----- shape ----------------------------------------------------------------------------------

    def size(self) -> int:
        return self._size

    def height(self) -> int:
        """Number of levels, counting the root. Every leaf is at this depth."""
        h, n = 0, self._root
        while True:
            h += 1
            if n.is_leaf():
                return h
            n = n.children[0]

    def node_count(self) -> int:
        return self._node_count(self._root)

    def _node_count(self, n: BTreeNode) -> int:
        return 1 + sum(self._node_count(ch) for ch in n.children)

    @staticmethod
    def height_bound(order: int, n: int) -> float:
        """Upper bound on height from the paper: 1 + log_(ceil(order/2)) ((n + 1) / 2)."""
        half_fan_out = (order + 1) // 2
        return 1 + math.log((n + 1) / 2.0) / math.log(half_fan_out)

    def check_invariants(self) -> bool:
        """Sorted keys, occupancy bounds, separator ranges, equal leaf depth. Raises on the first violation."""
        self._verify(self._root, None, None, 0, [-1])
        return True

    def _verify(self, n: BTreeNode, lo: Any, hi: Any, depth: int, leaf_depth: list) -> None:
        if n is not self._root and len(n.keys) < self.min_keys:
            raise IllegalStateError(f"underfull node: {n.keys}")
        if len(n.keys) > self.max_keys:
            raise IllegalStateError(f"overfull node: {n.keys}")
        if n is not self._root and len(n.keys) == 0:
            raise IllegalStateError("empty non-root node")
        for i, k in enumerate(n.keys):
            if i > 0 and n.keys[i - 1] >= k:
                raise IllegalStateError(f"keys not sorted: {n.keys}")
            if lo is not None and k <= lo:
                raise IllegalStateError(f"key {k} below separator {lo}")
            if hi is not None and k >= hi:
                raise IllegalStateError(f"key {k} above separator {hi}")
        if n.is_leaf():
            if leaf_depth[0] == -1:
                leaf_depth[0] = depth
            elif leaf_depth[0] != depth:
                raise IllegalStateError(f"leaves at different depths: {leaf_depth[0]} and {depth}")
            return
        if len(n.children) != len(n.keys) + 1:
            raise IllegalStateError(f"children != keys + 1 at {n.keys}")
        for i, child in enumerate(n.children):
            child_lo = lo if i == 0 else n.keys[i - 1]
            child_hi = hi if i == len(n.keys) else n.keys[i]
            self._verify(child, child_lo, child_hi, depth + 1, leaf_depth)


def java_list(xs: list) -> str:
    """List.toString: [1, 2, 3]."""
    return "[" + ", ".join(str(x) for x in xs) + "]"


def main() -> None:
    print("== Fan-out decides height: 10 million keys, one page read per level ==")
    print(f"binary tree: height ~ {fmt_fixed(math.log(10_000_000) / math.log(2), 1)} "
          "(one node per level: 23 dependent cache misses or disk reads)")
    for order in (8, 64, 400):
        print(f"order {order:4d}:  height <= {fmt_fixed(BTree.height_bound(order, 10_000_000), 1)}")
    print("PostgreSQL: 8 KB page, ~400 bigint keys per page -> 4 levels index billions of rows, and the top 3 live in RAM.")

    print()
    print("== A small tree you can draw: order 4, keys 1..10 ==")
    small: BTree[int, str] = BTree(4)
    for k in range(1, 11):
        small.put(k, f"v{k}")
    small.check_invariants()
    print(f"height={small.height()} nodes={small.node_count()} keys={java_list(small.keys())}")
    print(f"stats after 10 inserts: {small.stats}")

    print()
    print("== 200,000 payment ids, order 64: sequential vs random keys (UUIDv4-style) ==")
    n = 200_000
    sequential: BTree[int, int] = BTree(64)
    for pid in range(1, n + 1):
        sequential.put(pid, 1)
    sequential.check_invariants()
    print(f"sequential: height={sequential.height()} nodes={sequential.node_count()} {sequential.stats}")

    rng = JavaRandom(42)
    random_tree: BTree[int, int] = BTree(64)
    for _ in range(n):
        random_tree.put(rng.next_long() & LONG_MAX, 1)
    random_tree.check_invariants()
    print(f"random:     height={random_tree.height()} nodes={random_tree.node_count()} {random_tree.stats}")
    seq_fill = 100.0 * sequential.size() / (sequential.node_count() * 63.0)
    rnd_fill = 100.0 * random_tree.size() / (random_tree.node_count() * 63.0)
    print(f"occupancy: sequential ~{fmt_fixed(seq_fill, 0)}% (every split leaves a half-empty page behind), "
          f"random ~{fmt_fixed(rnd_fill, 0)}% (ln 2)")
    print("real databases special-case the rightmost split (PostgreSQL fillfactor 90%) to fix the sequential case.")

    print()
    print("== Point lookup and range scan cost in page reads ==")
    sequential.stats.reset()
    sequential.get(123_456)
    print(f"get(123456): {sequential.stats.reads} page reads (= height {sequential.height()})")
    sequential.stats.reset()
    scan = sequential.range(100_000, 100_999)
    print(f"range(100000, 100999): {len(scan)} keys in {sequential.stats.reads} page reads")

    print()
    print("== Deletes: borrow from a sibling or merge, leaves stay level ==")
    sequential.stats.reset()
    for pid in range(1, n + 1, 2):
        sequential.remove(pid)
    sequential.check_invariants()
    print(f"after removing every odd id: size={sequential.size()} height={sequential.height()} "
          f"nodes={sequential.node_count()} {sequential.stats}")

    print()
    print("== Shrinking all the way down ==")
    ids = list(range(5_000))
    java_shuffle(ids, JavaRandom(7))
    shrink: BTree[int, str] = BTree(6)
    for pid in ids:
        shrink.put(pid, "x")
    h0 = shrink.height()
    for pid in ids:
        shrink.remove(pid)
    shrink.check_invariants()
    print(f"5000 inserts then 5000 deletes in random order: height {h0} -> {shrink.height()}, size={shrink.size()}")


if __name__ == "__main__":
    main()
