"""Lesson: phases/01-complexity-and-core-structures/04-linked-lists/docs/en.md
Linked Lists: Singly, Doubly, and Intrusive.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Knuth, TAOCP Vol. 1, 2.2.3-2.2.5 (linked allocation, circular and doubly linked lists);
Linux kernel include/linux/list.h (intrusive list_head). Python 3.11+, standard library only.
"""

from __future__ import annotations

import time
from typing import Callable, Generic, Iterator, Optional, TypeVar

T = TypeVar("T")


class SinglyLinkedList(Generic[T]):
    """Singly linked list with head and tail pointers.
    O(1): add_first, add_last, remove_first. O(n): get, remove by value, remove_last (no back pointer)."""

    class _Node:
        __slots__ = ("value", "next")

        def __init__(self, value) -> None:
            self.value = value
            self.next = None

    def __init__(self) -> None:
        self._head: Optional[SinglyLinkedList._Node] = None
        self._tail: Optional[SinglyLinkedList._Node] = None
        self._size = 0

    def size(self) -> int:
        return self._size

    def is_empty(self) -> bool:
        return self._size == 0

    def add_first(self, value: T) -> None:
        n = SinglyLinkedList._Node(value)
        n.next = self._head
        self._head = n
        if self._tail is None:
            self._tail = n
        self._size += 1

    def add_last(self, value: T) -> None:
        n = SinglyLinkedList._Node(value)
        if self._tail is None:
            self._head = self._tail = n
        else:
            self._tail.next = n
            self._tail = n
        self._size += 1

    def remove_first(self) -> T:
        if self._head is None:
            raise IndexError("empty")  # Java: NoSuchElementException
        v = self._head.value
        self._head = self._head.next
        if self._head is None:
            self._tail = None
        self._size -= 1
        return v

    def get(self, index: int) -> T:
        """O(n): walk from the head. There is no shortcut; this is the operation arrays do in O(1)."""
        if index < 0 or index >= self._size:
            raise IndexError(f"Index {index} out of bounds for length {self._size}")
        n = self._head
        for _ in range(index):
            n = n.next
        return n.value

    def remove_first_occurrence(self, value: T) -> bool:
        """O(n): find the node *before* the match so it can be unlinked."""
        prev, cur = None, self._head
        while cur is not None:
            if cur.value == value:
                if prev is None:
                    self._head = cur.next
                else:
                    prev.next = cur.next
                if cur is self._tail:
                    self._tail = prev
                self._size -= 1
                return True
            prev = cur
            cur = cur.next
        return False

    def reverse(self) -> None:
        """In-place reversal with three pointers; O(n) time, O(1) extra space."""
        prev, cur = None, self._head
        self._tail = self._head
        while cur is not None:
            nxt = cur.next
            cur.next = prev
            prev = cur
            cur = nxt
        self._head = prev

    def __iter__(self) -> Iterator[T]:
        cur = self._head
        while cur is not None:
            yield cur.value
            cur = cur.next

    def __str__(self) -> str:
        return "[" + ", ".join(str(v) for v in self) + "]"


class DoublyLinkedList(Generic[T]):
    """Doubly linked list with a sentinel node. The sentinel's next is the first element and its prev is the last,
    so there is never a None to check. Handing out Node references makes unlink(node) and move_to_front(node)
    O(1): the basis of every LRU cache."""

    class Node:
        __slots__ = ("value", "prev", "next", "owner")

        def __init__(self, value) -> None:
            self.value = value
            self.prev: Optional[DoublyLinkedList.Node] = None
            self.next: Optional[DoublyLinkedList.Node] = None
            self.owner: Optional[DoublyLinkedList] = None  # which list this node is linked into; None once unlinked

    def __init__(self) -> None:
        self._sentinel = DoublyLinkedList.Node(None)
        self._sentinel.prev = self._sentinel.next = self._sentinel
        self._size = 0

    def size(self) -> int:
        return self._size

    def is_empty(self) -> bool:
        return self._size == 0

    def first(self) -> Optional[Node]:
        return None if self._size == 0 else self._sentinel.next

    def last(self) -> Optional[Node]:
        return None if self._size == 0 else self._sentinel.prev

    def _link_before(self, value: T, succ: Node) -> Node:
        """Link a fresh node holding `value` immediately before `succ`. Works for every position."""
        n = DoublyLinkedList.Node(value)
        pred = succ.prev
        n.prev = pred
        n.next = succ
        pred.next = n
        succ.prev = n
        n.owner = self
        self._size += 1
        return n

    def add_first(self, value: T) -> Node:
        return self._link_before(value, self._sentinel.next)

    def add_last(self, value: T) -> Node:
        return self._link_before(value, self._sentinel)

    def insert_before(self, node: Node, value: T) -> Node:
        self._require_owned(node)
        return self._link_before(value, node)

    def unlink(self, node: Node) -> T:
        """O(1): the node knows both neighbours, so no search is needed."""
        self._require_owned(node)
        node.prev.next = node.next
        node.next.prev = node.prev
        node.prev = node.next = None
        node.owner = None
        self._size -= 1
        return node.value

    def remove_first(self) -> T:
        if self._size == 0:
            raise IndexError("empty")
        return self.unlink(self._sentinel.next)

    def remove_last(self) -> T:
        if self._size == 0:
            raise IndexError("empty")
        return self.unlink(self._sentinel.prev)

    def move_to_front(self, node: Node) -> None:
        """O(1) relink without allocation: the LRU "touch" operation."""
        self._require_owned(node)
        s = self._sentinel
        if s.next is node:
            return
        node.prev.next = node.next
        node.next.prev = node.prev
        node.prev = s
        node.next = s.next
        s.next.prev = node
        s.next = node

    def splice_all(self, other: DoublyLinkedList[T]) -> None:
        """O(1) relink regardless of either list's length; ownership transfer is the one O(n) walk."""
        if other is self:
            raise ValueError("cannot splice a list into itself")
        if other._size == 0:
            return
        s = self._sentinel
        other_first = other._sentinel.next
        other_last = other._sentinel.prev
        my_last = s.prev
        my_last.next = other_first
        other_first.prev = my_last
        other_last.next = s
        s.prev = other_last
        n = other_first
        while n is not s:
            n.owner = self
            n = n.next
        self._size += other._size
        other._sentinel.next = other._sentinel.prev = other._sentinel
        other._size = 0

    def _require_owned(self, node: Optional[Node]) -> None:
        if node is None or node.owner is not self:
            raise ValueError("node is not linked into this list")

    def invariant_holds(self) -> bool:
        """Forward and backward walks agree with each other and with size."""
        s = self._sentinel
        forward = 0
        n = s.next
        while n is not s:
            if n.next.prev is not n or n.owner is not self:
                return False
            forward += 1
            n = n.next
        backward = 0
        n = s.prev
        while n is not s:
            backward += 1
            n = n.prev
        return forward == self._size and backward == self._size

    def __iter__(self) -> Iterator[T]:
        s = self._sentinel
        cur = s.next
        while cur is not s:
            yield cur.value
            cur = cur.next

    def __str__(self) -> str:
        return "[" + ", ".join(str(v) for v in self) + "]"


class IntrusiveNode:
    """Base class for elements of an intrusive list: the element carries its own links, so there is no separate
    node object, no allocation on insert, and removal is O(1) given only the element."""

    __slots__ = ("prev", "next", "owner")

    def __init__(self) -> None:
        self.prev: Optional[IntrusiveNode] = None
        self.next: Optional[IntrusiveNode] = None
        self.owner: Optional[IntrusiveList] = None

    def is_linked(self) -> bool:
        return self.owner is not None


class _Sentinel(IntrusiveNode):
    __slots__ = ()


class IntrusiveList(Generic[T]):
    """Intrusive doubly linked list with a sentinel. Compare Linux's list_head."""

    def __init__(self) -> None:
        self._sentinel = _Sentinel()
        self._sentinel.prev = self._sentinel.next = self._sentinel
        self._size = 0

    def size(self) -> int:
        return self._size

    def is_empty(self) -> bool:
        return self._size == 0

    def add_last(self, element: IntrusiveNode) -> None:
        if element.owner is not None:
            raise RuntimeError("element is already in a list")  # Java: IllegalStateException
        s = self._sentinel
        last = s.prev
        element.prev = last
        element.next = s
        last.next = element
        s.prev = element
        element.owner = self
        self._size += 1

    def remove(self, element: IntrusiveNode) -> None:
        """O(1), no lookup: the element is its own handle."""
        if element.owner is not self:
            raise ValueError("element is not in this list")
        element.prev.next = element.next
        element.next.prev = element.prev
        element.prev = element.next = None
        element.owner = None
        self._size -= 1

    def contains(self, element: IntrusiveNode) -> bool:
        """O(1) membership test, because ownership is recorded in the element."""
        return element.owner is self

    def clear(self) -> None:
        """Detach every element so it can be linked elsewhere. O(n): each element must forget its owner."""
        s = self._sentinel
        cur = s.next
        while cur is not s:
            nxt = cur.next
            cur.prev = cur.next = None
            cur.owner = None
            cur = nxt
        s.prev = s.next = s
        self._size = 0

    def first(self):
        return None if self._size == 0 else self._sentinel.next

    def __iter__(self) -> Iterator:
        s = self._sentinel
        cur = s.next
        while cur is not s:
            nxt = cur.next
            yield cur
            cur = nxt


class PendingAuthorization(IntrusiveNode):
    """A pending card authorization awaiting capture or expiry, living in an intrusive list."""

    __slots__ = ("id", "amount_minor", "expires_at_millis")

    def __init__(self, id: str, amount_minor: int, expires_at_millis: int) -> None:
        super().__init__()
        self.id = id
        self.amount_minor = amount_minor  # integer minor units, never a float (Phase 10, Lesson 01)
        self.expires_at_millis = expires_at_millis

    def __str__(self) -> str:
        return f"{self.id}({self.amount_minor})"


class Cell(IntrusiveNode):
    """A cell used by the traversal benchmark: linked intrusively so allocation order and list order can differ."""

    __slots__ = ("value",)

    def __init__(self, value: int) -> None:
        # Links set inline rather than via super().__init__(): the demo allocates two million cells.
        self.prev = self.next = self.owner = None
        self.value = value


class JavaRandom:
    """java.util.Random, bit for bit (48-bit LCG), so shuffled() produces the same permutation as Java."""

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


def sum_array(values: list[int]) -> int:
    total = 0
    for v in values:
        total += v
    return total


def sum_list(lst: IntrusiveList) -> int:
    total = 0
    for c in lst:
        total += c.value
    return total


def link_in_order(cells: list[Cell], order: list[int]) -> IntrusiveList:
    """Build an intrusive list over pre-allocated cells, linking them in the given order."""
    lst: IntrusiveList = IntrusiveList()
    for i in order:
        lst.add_last(cells[i])
    return lst


def identity(n: int) -> list[int]:
    return list(range(n))


def shuffled(n: int, seed: int) -> list[int]:
    """Fisher-Yates with java.util.Random(seed): the same permutation the Java version produces."""
    order = identity(n)
    rnd = JavaRandom(seed)
    for i in range(n - 1, 0, -1):
        j = rnd.next_int(i + 1)
        order[i], order[j] = order[j], order[i]
    return order


def best_of(rounds: int, work: Callable[[], int]) -> int:
    best = None
    for _ in range(rounds):
        t0 = time.perf_counter_ns()
        work()
        elapsed = time.perf_counter_ns() - t0
        best = elapsed if best is None else min(best, elapsed)
    return best


def main() -> None:
    print("== Singly linked: O(1) at the head, O(n) everywhere else ==")
    singly: SinglyLinkedList[str] = SinglyLinkedList()
    singly.add_last("auth-1")
    singly.add_last("auth-2")
    singly.add_last("auth-3")
    singly.add_first("auth-0")
    print(f"list     = {singly} size={singly.size()}")
    print(f"get(2)   = {singly.get(2)}  (walked 2 links to find it)")
    singly.remove_first_occurrence("auth-2")
    print(f"remove auth-2 by value -> {singly}  (walked from the head to find the predecessor)")
    singly.reverse()
    print(f"reverse  = {singly}")
    print()

    print("== Doubly linked with a sentinel: O(1) unlink given the node ==")
    lru: DoublyLinkedList[str] = DoublyLinkedList()
    k1 = lru.add_last("idem-key-1")
    k2 = lru.add_last("idem-key-2")
    k3 = lru.add_last("idem-key-3")
    print(f"recency order (front = most recent): {lru}")
    lru.move_to_front(k3)
    print(f"touch key-3 -> {lru}")
    lru.move_to_front(k1)
    print(f"touch key-1 -> {lru}")
    print(f"evict least recent (removeLast) -> {lru.remove_last()}, now {lru}")
    print(f"unlink key-3 directly -> {lru.unlink(k3)}, now {lru} size={lru.size()}")
    print(f"k2 still linked? {str(k2.owner is not None).lower()}; invariant holds? {str(lru.invariant_holds()).lower()}")
    print()

    print("== Splice: merge two queues in O(1), not O(n) ==")
    shard_a: DoublyLinkedList[str] = DoublyLinkedList()
    shard_b: DoublyLinkedList[str] = DoublyLinkedList()
    for i in range(1, 4):
        shard_a.add_last(f"a{i}")
    for i in range(1, 5):
        shard_b.add_last(f"b{i}")
    shard_a.splice_all(shard_b)
    print(f"A after splice = {shard_a} size={shard_a.size()}; B = {shard_b} size={shard_b.size()}")
    print()

    print("== Intrusive list: the element is its own node ==")
    pending: IntrusiveList = IntrusiveList()
    a1 = PendingAuthorization("auth-7f3a", 1999, 1_000)
    a2 = PendingAuthorization("auth-9c21", 4500, 2_000)
    a3 = PendingAuthorization("auth-0b77", 250, 3_000)
    pending.add_last(a1)
    pending.add_last(a2)
    pending.add_last(a3)
    print("pending: " + " ".join(str(p) for p in pending))
    print(f"capture auth-9c21: remove(a2) is O(1) with no search; linked before? {str(pending.contains(a2)).lower()}")
    pending.remove(a2)
    print("pending: " + " ".join(str(p) for p in pending) + f"  linked after? {str(pending.contains(a2)).lower()}")
    try:
        pending.remove(a2)
    except ValueError as e:
        print(f"remove twice -> {e}")
    print()

    print("== Measured: summing 1,000,000 longs, array vs linked list (sequential vs shuffled memory order) ==")
    n = 1_000_000
    array = list(range(n))
    cells_a = [Cell(i) for i in range(n)]
    cells_b = [Cell(i) for i in range(n)]
    sequential = link_in_order(cells_a, identity(n))          # list order == allocation order
    scattered = link_in_order(cells_b, shuffled(n, 42))       # list order is a random walk through memory
    expected = n * (n - 1) // 2
    # Best of 3 rather than Java's 5: the interpreted loop is slower, and the demo must finish in ten seconds.
    t_array = best_of(3, lambda: sum_array(array))
    t_seq = best_of(3, lambda: sum_list(sequential))
    t_scat = best_of(3, lambda: sum_list(scattered))
    ok = sum_array(array) == expected and sum_list(sequential) == expected and sum_list(scattered) == expected
    print(f"all three sums equal {expected}: {str(ok).lower()}")
    print("long[] sequential scan : %6.2f ms" % (t_array / 1e6))
    print("list, nodes in memory order : %6.2f ms  (%.1fx the array)" % (t_seq / 1e6, t_seq / t_array))
    print("list, nodes in shuffled order: %6.2f ms  (%.1fx the array)" % (t_scat / 1e6, t_scat / t_array))
    print("Same n, same O(n), same sum. The difference is cache misses; timings vary by machine, the ordering does not.")


if __name__ == "__main__":
    main()
