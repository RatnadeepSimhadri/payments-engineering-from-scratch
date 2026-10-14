"""Lesson: phases/03-systems-data-structures/03-balanced-trees/docs/en.md
Balanced Trees: AVL and Red-Black
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Adelson-Velsky & Landis (1962); Guibas & Sedgewick (1978); CLRS ch. 13 (RB-INSERT-FIXUP).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import math
from decimal import ROUND_HALF_UP, Decimal
from typing import Any, Generic, Optional, TypeVar

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


def java_shuffle(xs: list, rng: JavaRandom) -> None:
    """Collections.shuffle(list, rnd) for a RandomAccess list: swap(i - 1, nextInt(i)) from the end."""
    for i in range(len(xs), 1, -1):
        j = rng.next_int(i)
        xs[i - 1], xs[j] = xs[j], xs[i - 1]


class NaiveBst(Generic[K, V]):
    """Unbalanced BST, kept only to show what balancing prevents: sorted inserts give height n."""

    class Node:
        __slots__ = ("key", "value", "left", "right")

        def __init__(self, key: Any, value: Any) -> None:
            self.key = key
            self.value = value
            self.left: Optional[NaiveBst.Node] = None
            self.right: Optional[NaiveBst.Node] = None

    def __init__(self) -> None:
        self._root: Optional[NaiveBst.Node] = None
        self._size = 0

    def put(self, key: K, value: V) -> None:
        if self._root is None:
            self._root = NaiveBst.Node(key, value)
            self._size += 1
            return
        cur = self._root
        while True:
            if key == cur.key:
                cur.value = value
                return
            nxt = cur.left if key < cur.key else cur.right
            if nxt is None:
                if key < cur.key:
                    cur.left = NaiveBst.Node(key, value)
                else:
                    cur.right = NaiveBst.Node(key, value)
                self._size += 1
                return
            cur = nxt

    def size(self) -> int:
        return self._size

    def height(self) -> int:
        """Iterative (level by level): a degenerate tree of 2,000 nodes would blow Python's recursion limit."""
        level = [self._root] if self._root is not None else []
        h = 0
        while level:
            h += 1
            level = [c for n in level for c in (n.left, n.right) if c is not None]
        return h


class AvlNode:
    __slots__ = ("key", "value", "height", "left", "right")

    def __init__(self, key: Any, value: Any) -> None:
        self.key = key
        self.value = value
        self.height = 1
        self.left: Optional[AvlNode] = None
        self.right: Optional[AvlNode] = None


class AvlTree(Generic[K, V]):
    """AVL tree: |height(left) - height(right)| <= 1 at every node, heights cached in nodes."""

    Node = AvlNode

    def __init__(self) -> None:
        self._root: Optional[AvlNode] = None
        self._size = 0
        self._rotations = 0

    @staticmethod
    def node_height(n: Optional[AvlNode]) -> int:
        return 0 if n is None else n.height

    @staticmethod
    def balance_factor(n: Optional[AvlNode]) -> int:
        return 0 if n is None else AvlTree.node_height(n.left) - AvlTree.node_height(n.right)

    @staticmethod
    def _update(n: AvlNode) -> None:
        hl = 0 if n.left is None else n.left.height
        hr = 0 if n.right is None else n.right.height
        n.height = 1 + (hl if hl > hr else hr)

    def rotate_right(self, y: AvlNode) -> AvlNode:
        """y's left child x becomes the subtree root; x's right subtree moves under y."""
        x = y.left
        y.left = x.right
        x.right = y
        self._update(y)
        self._update(x)
        self._rotations += 1
        return x

    def rotate_left(self, x: AvlNode) -> AvlNode:
        """x's right child y becomes the subtree root; y's left subtree moves under x."""
        y = x.right
        x.right = y.left
        y.left = x
        self._update(x)
        self._update(y)
        self._rotations += 1
        return y

    def _rebalance(self, n: AvlNode) -> AvlNode:
        """Restore |balance| <= 1 at n. LL and RR need one rotation, LR and RL need two."""
        self._update(n)
        bf = self.balance_factor(n)
        if bf > 1:
            if self.balance_factor(n.left) < 0:
                n.left = self.rotate_left(n.left)    # LR: straighten first
            return self.rotate_right(n)              # LL
        if bf < -1:
            if self.balance_factor(n.right) > 0:
                n.right = self.rotate_right(n.right)  # RL: straighten first
            return self.rotate_left(n)                # RR
        return n

    def put(self, key: K, value: V) -> None:
        self._root = self._put(self._root, key, value)

    def _put(self, n: Optional[AvlNode], key: K, value: V) -> AvlNode:
        if n is None:
            self._size += 1
            return AvlNode(key, value)
        if key < n.key:
            n.left = self._put(n.left, key, value)
        elif key > n.key:
            n.right = self._put(n.right, key, value)
        else:
            n.value = value
            return n
        return self._rebalance(n)

    def get(self, key: K) -> Optional[V]:
        cur = self._root
        while cur is not None:
            if key == cur.key:
                return cur.value
            cur = cur.left if key < cur.key else cur.right
        return None

    def contains_key(self, key: K) -> bool:
        return self.get(key) is not None

    def remove(self, key: K) -> None:
        self._root = self._remove(self._root, key)

    def _remove(self, n: Optional[AvlNode], key: K) -> Optional[AvlNode]:
        if n is None:
            return None
        if key < n.key:
            n.left = self._remove(n.left, key)
        elif key > n.key:
            n.right = self._remove(n.right, key)
        else:
            if n.left is None or n.right is None:     # zero or one child: splice out
                self._size -= 1
                return n.right if n.left is None else n.left
            successor = n.right                       # two children: replace with in-order successor
            while successor.left is not None:
                successor = successor.left
            n.key = successor.key
            n.value = successor.value
            n.right = self._remove(n.right, successor.key)
        return self._rebalance(n)

    def ceiling_key(self, key: K) -> Optional[K]:
        """Smallest key >= key, or None: the operation a hash map cannot do."""
        cur, best = self._root, None
        while cur is not None:
            if key == cur.key:
                return cur.key
            if key < cur.key:
                best = cur.key
                cur = cur.left
            else:
                cur = cur.right
        return best

    def range(self, lo: K, hi: K) -> list:
        """Keys in [lo, hi], ascending, visiting only the subtrees that can contain them."""
        out: list = []
        self._range(self._root, lo, hi, out)
        return out

    def _range(self, n: Optional[AvlNode], lo: K, hi: K, out: list) -> None:
        if n is None:
            return
        if lo < n.key:
            self._range(n.left, lo, hi, out)
        if lo <= n.key <= hi:
            out.append(n.key)
        if hi > n.key:
            self._range(n.right, lo, hi, out)

    def keys(self) -> list:
        out: list = []
        self._inorder(self._root, out)
        return out

    def _inorder(self, n: Optional[AvlNode], out: list) -> None:
        if n is None:
            return
        self._inorder(n.left, out)
        out.append(n.key)
        self._inorder(n.right, out)

    def size(self) -> int:
        return self._size

    def height(self) -> int:
        return self.node_height(self._root)

    def rotations(self) -> int:
        return self._rotations

    @staticmethod
    def height_bound(n: int) -> float:
        """AVL analysis: height < 1.4405 * log2(n + 2) - 0.3277."""
        return 1.4405 * math.log(n + 2) / math.log(2) - 0.3277

    def check_invariants(self) -> bool:
        AvlTree.verify(self._root, None, None)
        return True

    @staticmethod
    def verify(n: Optional[AvlNode], lo: Any, hi: Any) -> int:
        """BST order, stored height correct, |balance| <= 1. Raises IllegalStateError on the first violation."""
        if n is None:
            return 0
        if lo is not None and n.key <= lo:
            raise IllegalStateError(f"BST order violated at {n.key}")
        if hi is not None and n.key >= hi:
            raise IllegalStateError(f"BST order violated at {n.key}")
        hl = AvlTree.verify(n.left, lo, n.key)
        hr = AvlTree.verify(n.right, n.key, hi)
        if n.height != 1 + max(hl, hr):
            raise IllegalStateError(f"stale height at {n.key}")
        if abs(hl - hr) > 1:
            raise IllegalStateError(f"AVL balance violated at {n.key}")
        return n.height


class IllegalStateError(Exception):
    """Java's IllegalStateException: an invariant checker found a corrupted tree."""


RED = True
BLACK = False


class RbNode:
    __slots__ = ("key", "value", "color", "left", "right", "parent")

    def __init__(self, key: Any, value: Any, parent: Optional[RbNode]) -> None:
        self.key = key
        self.value = value
        self.color = RED
        self.left: Optional[RbNode] = None
        self.right: Optional[RbNode] = None
        self.parent = parent


def is_red(n: Optional[RbNode]) -> bool:
    return n is not None and n.color == RED


class RedBlackTree(Generic[K, V]):
    """Red-black tree with the CLRS insert fix-up. Height <= 2 * log2(n + 1)."""

    RED = RED
    BLACK = BLACK
    Node = RbNode

    def __init__(self) -> None:
        self._root: Optional[RbNode] = None
        self._size = 0
        self._rotations = 0
        self._recolorings = 0

    def _rotate_left(self, x: RbNode) -> None:
        y = x.right
        x.right = y.left
        if y.left is not None:
            y.left.parent = x
        y.parent = x.parent
        if x.parent is None:
            self._root = y
        elif x is x.parent.left:
            x.parent.left = y
        else:
            x.parent.right = y
        y.left = x
        x.parent = y
        self._rotations += 1

    def _rotate_right(self, y: RbNode) -> None:
        x = y.left
        y.left = x.right
        if x.right is not None:
            x.right.parent = y
        x.parent = y.parent
        if y.parent is None:
            self._root = x
        elif y is y.parent.right:
            y.parent.right = x
        else:
            y.parent.left = x
        x.right = y
        y.parent = x
        self._rotations += 1

    def put(self, key: K, value: V) -> None:
        parent, cur = None, self._root
        less = False
        while cur is not None:
            parent = cur
            if key == cur.key:
                cur.value = value
                return
            less = key < cur.key
            cur = cur.left if less else cur.right
        z = RbNode(key, value, parent)
        if parent is None:
            self._root = z
        elif less:
            parent.left = z
        else:
            parent.right = z
        self._size += 1
        self._insert_fixup(z)

    def _insert_fixup(self, z: RbNode) -> None:
        """Case 1 uncle red: recolor, move up. Case 2 inner grandchild: rotate parent. Case 3: recolor, rotate g."""
        while is_red(z.parent):
            p = z.parent
            g = p.parent  # exists: a red parent is never the root
            if p is g.left:
                uncle = g.right
                if is_red(uncle):                                  # case 1
                    p.color = BLACK
                    uncle.color = BLACK
                    g.color = RED
                    self._recolorings += 3
                    z = g
                else:
                    if z is p.right:                               # case 2 -> 3
                        z = p
                        self._rotate_left(z)
                        p = z.parent
                    p.color = BLACK                                # case 3
                    g.color = RED
                    self._recolorings += 2
                    self._rotate_right(g)
            else:
                uncle = g.left
                if is_red(uncle):
                    p.color = BLACK
                    uncle.color = BLACK
                    g.color = RED
                    self._recolorings += 3
                    z = g
                else:
                    if z is p.left:
                        z = p
                        self._rotate_right(z)
                        p = z.parent
                    p.color = BLACK
                    g.color = RED
                    self._recolorings += 2
                    self._rotate_left(g)
        if self._root.color == RED:
            self._root.color = BLACK
            self._recolorings += 1

    def get(self, key: K) -> Optional[V]:
        cur = self._root
        while cur is not None:
            if key == cur.key:
                return cur.value
            cur = cur.left if key < cur.key else cur.right
        return None

    def keys(self) -> list:
        out: list = []
        self._inorder(self._root, out)
        return out

    def _inorder(self, n: Optional[RbNode], out: list) -> None:
        if n is None:
            return
        self._inorder(n.left, out)
        out.append(n.key)
        self._inorder(n.right, out)

    def size(self) -> int:
        return self._size

    def rotations(self) -> int:
        return self._rotations

    def recolorings(self) -> int:
        return self._recolorings

    def height(self) -> int:
        return self._height(self._root)

    def _height(self, n: Optional[RbNode]) -> int:
        return 0 if n is None else 1 + max(self._height(n.left), self._height(n.right))

    def black_height(self) -> int:
        h = 0
        n = self._root
        while n is not None:
            if n.color == BLACK:
                h += 1
            n = n.left
        return h

    @staticmethod
    def height_bound(n: int) -> float:
        """Red-black analysis: height <= 2 * log2(n + 1)."""
        return 2.0 * math.log(n + 1) / math.log(2)

    def check_invariants(self) -> bool:
        if is_red(self._root):
            raise IllegalStateError("root is red")
        self._verify(self._root, None, None)
        return True

    def _verify(self, n: Optional[RbNode], lo: Any, hi: Any) -> int:
        if n is None:
            return 1  # null leaves count as one black
        if lo is not None and n.key <= lo:
            raise IllegalStateError(f"BST order violated at {n.key}")
        if hi is not None and n.key >= hi:
            raise IllegalStateError(f"BST order violated at {n.key}")
        if is_red(n) and (is_red(n.left) or is_red(n.right)):
            raise IllegalStateError(f"red node with red child at {n.key}")
        if n.left is not None and n.left.parent is not n:
            raise IllegalStateError(f"broken parent link at {n.left.key}")
        if n.right is not None and n.right.parent is not n:
            raise IllegalStateError(f"broken parent link at {n.right.key}")
        bl = self._verify(n.left, lo, n.key)
        br = self._verify(n.right, n.key, hi)
        if bl != br:
            raise IllegalStateError(f"black height differs at {n.key}: {bl} vs {br}")
        return bl + (1 if n.color == BLACK else 0)


def log2(n: int) -> int:
    """32 - Integer.numberOfLeadingZeros(n), i.e. the bit length."""
    return n.bit_length()


def main() -> None:
    n = 100_000

    print("== Sorted inserts: what a plain BST does with timestamps ==")
    naive: NaiveBst[int, str] = NaiveBst()
    for i in range(2_000):
        naive.put(i, "auth")
    print(f"naive BST, 2000 sorted keys: height={naive.height()} (a linked list; log2(2000)={log2(2000)})")

    print()
    print(f"== AVL vs red-black on {n} sorted keys (expiry timestamps arriving in order) ==")
    avl: AvlTree[int, str] = AvlTree()
    rb: RedBlackTree[int, str] = RedBlackTree()
    for i in range(n):
        avl.put(i, "auth")
        rb.put(i, "auth")
    avl.check_invariants()
    rb.check_invariants()
    print(f"AVL        height={avl.height()}  bound={fmt_fixed(AvlTree.height_bound(n), 1)}  rotations={avl.rotations()}")
    print(f"red-black  height={rb.height()}  bound={fmt_fixed(RedBlackTree.height_bound(n), 1)}  "
          f"rotations={rb.rotations()}  recolorings={rb.recolorings()}  black-height={rb.black_height()}")

    print()
    print("== Same keys, random order (seeded) ==")
    shuffled = list(range(n))
    java_shuffle(shuffled, JavaRandom(2024))
    avl2: AvlTree[int, str] = AvlTree()
    rb2: RedBlackTree[int, str] = RedBlackTree()
    for k in shuffled:
        avl2.put(k, "auth")
        rb2.put(k, "auth")
    avl2.check_invariants()
    rb2.check_invariants()
    print(f"AVL        height={avl2.height()}  rotations={avl2.rotations()}")
    print(f"red-black  height={rb2.height()}  rotations={rb2.rotations()}  recolorings={rb2.recolorings()}")

    print()
    print("== The operation a hash map cannot do: expiries between two instants ==")
    expiries: AvlTree[int, str] = AvlTree()
    t0 = 1_700_000_000_000
    rng = JavaRandom(7)
    for i in range(20):
        expiries.put(t0 + rng.next_int(60_000), f"auth_{i}")
    print(f"expiries in [t0+10s, t0+20s): {[t - t0 for t in expiries.range(t0 + 10_000, t0 + 19_999)]}")
    print(f"first expiry at or after t0+30s: +{expiries.ceiling_key(t0 + 30_000) - t0} ms")

    print()
    print("== Deletes keep the AVL invariant ==")
    for i in range(0, n, 2):
        avl.remove(i)
    avl.check_invariants()
    print(f"after removing every even key: size={avl.size()} height={avl.height()}, invariants hold")


if __name__ == "__main__":
    main()
