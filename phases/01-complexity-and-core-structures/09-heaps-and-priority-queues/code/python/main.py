"""Lesson: phases/01-complexity-and-core-structures/09-heaps-and-priority-queues/docs/en.md
Heaps and Priority Queues from Scratch
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Williams, "Algorithm 232: Heapsort" (CACM 1964); Floyd, "Algorithm 245: Treesort 3" (CACM 1964, the
         O(n) heapify); CLRS ch. 6. FakeClock and SeededRandom copied from Phase 0, Lesson 03.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Callable, Generic, Hashable, Iterable, Optional, TypeVar

T = TypeVar("T")
K = TypeVar("K", bound=Hashable)

_MASK64 = (1 << 64) - 1


class FakeClock:
    """A clock that moves only when told to. Copied from Phase 0, Lesson 03."""

    def __init__(self, start_millis: int) -> None:
        self._now = start_millis

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError(f"cannot advance by a negative duration: {millis}")
        self._now += millis


class SeededRandom:
    """SplitMix64, copied from Phase 0, Lesson 03: the same sequence for the same seed in Java, Python, and Go."""

    def __init__(self, seed: int) -> None:
        self._state = seed & _MASK64

    def _next_u64(self) -> int:
        self._state = (self._state + 0x9E3779B97F4A7C15) & _MASK64
        z = self._state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & _MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & _MASK64
        return z ^ (z >> 31)

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError(f"bound must be positive: {bound}")
        return (self._next_u64() >> 33) % bound


class MinHeap(Generic[T]):
    """A binary min-heap in a list. Invariant: for every index i > 0, compare(items[(i-1)//2], items[i]) <= 0.
    The root (index 0) is therefore the minimum. Heap order is partial: siblings are unordered."""

    def __init__(self, compare: Callable[[T, T], int]) -> None:
        self._items: list[T] = []
        self._compare = compare

    @staticmethod
    def heapify(source: Iterable[T], compare: Callable[[T, T], int]) -> "MinHeap[T]":
        """Floyd's bottom-up construction: sift down every internal node from the last parent to the root. O(n)."""
        heap: MinHeap[T] = MinHeap(compare)
        heap._items = list(source)
        for i in range(len(heap._items) // 2 - 1, -1, -1):
            heap._sift_down(i)
        return heap

    def push(self, item: T) -> None:
        self._items.append(item)
        self._sift_up(len(self._items) - 1)

    def peek(self) -> T:
        if not self._items:
            raise IndexError("heap is empty")
        return self._items[0]

    def pop(self) -> T:
        if not self._items:
            raise IndexError("heap is empty")
        top = self._items[0]
        last = self._items.pop()
        if self._items:
            self._items[0] = last
            self._sift_down(0)
        return top

    def size(self) -> int:
        return len(self._items)

    def is_empty(self) -> bool:
        return not self._items

    def _sift_up(self, i: int) -> None:
        items, cmp = self._items, self._compare
        while i > 0:
            parent = (i - 1) // 2
            if cmp(items[parent], items[i]) <= 0:
                return
            items[parent], items[i] = items[i], items[parent]
            i = parent

    def _sift_down(self, i: int) -> None:
        items, cmp = self._items, self._compare
        n = len(items)
        while True:
            left, right, smallest = 2 * i + 1, 2 * i + 2, i
            if left < n and cmp(items[left], items[smallest]) < 0:
                smallest = left
            if right < n and cmp(items[right], items[smallest]) < 0:
                smallest = right
            if smallest == i:
                return
            items[i], items[smallest] = items[smallest], items[i]
            i = smallest

    def is_heap(self) -> bool:
        """Checks the invariant over the whole list. O(n); for tests and assertions, not for production paths."""
        items, cmp = self._items, self._compare
        return all(cmp(items[(i - 1) // 2], items[i]) <= 0 for i in range(1, len(items)))


def natural_order(a, b) -> int:
    return -1 if a < b else (1 if a > b else 0)


def reverse_order(a, b) -> int:
    return natural_order(b, a)


@dataclass(frozen=True)
class Entry(Generic[K]):
    """What an indexed heap stores: a unique key, its priority, and the sequence number that breaks ties."""

    key: K
    priority: int
    seq: int

    @staticmethod
    def compare(a: "Entry[K]", b: "Entry[K]") -> int:
        """Lower priority first; equal priorities fall back to insertion order, which makes pop() stable."""
        if a.priority != b.priority:
            return -1 if a.priority < b.priority else 1
        return natural_order(a.seq, b.seq)


class IndexedMinHeap(Generic[K]):
    """A min-heap of unique keys with a position index, so decrease_key(key) is O(log n) instead of O(n).
    Invariant: index[entries[i].key] == i for every i, in addition to heap order."""

    def __init__(self) -> None:
        self._entries: list[Entry[K]] = []
        self._index: dict[K, int] = {}
        self._next_seq = 0

    def push(self, key: K, priority: int) -> None:
        if key in self._index:
            raise ValueError(f"key already present: {key!r}")
        self._entries.append(Entry(key, priority, self._next_seq))
        self._next_seq += 1
        self._index[key] = len(self._entries) - 1
        self._sift_up(len(self._entries) - 1)

    def decrease_key(self, key: K, new_priority: int) -> None:
        """Lower the priority of an existing key. The entry keeps its seq, so its tie-break rank is unchanged."""
        i = self._index.get(key)
        if i is None:
            raise KeyError(f"key not present: {key!r}")
        current = self._entries[i]
        if new_priority > current.priority:
            raise ValueError(f"decrease_key cannot raise {key!r} from {current.priority} to {new_priority}")
        self._entries[i] = Entry(key, new_priority, current.seq)
        self._sift_up(i)

    def peek(self) -> Entry[K]:
        if not self._entries:
            raise IndexError("heap is empty")
        return self._entries[0]

    def pop(self) -> Entry[K]:
        if not self._entries:
            raise IndexError("heap is empty")
        top = self._entries[0]
        last = self._entries.pop()
        del self._index[top.key]
        if self._entries:
            self._entries[0] = last
            self._index[last.key] = 0
            self._sift_down(0)
        return top

    def contains(self, key: K) -> bool:
        return key in self._index

    def priority_of(self, key: K) -> int:
        i = self._index.get(key)
        if i is None:
            raise KeyError(f"key not present: {key!r}")
        return self._entries[i].priority

    def size(self) -> int:
        return len(self._entries)

    def is_empty(self) -> bool:
        return not self._entries

    def _sift_up(self, i: int) -> None:
        e = self._entries
        while i > 0:
            parent = (i - 1) // 2
            if Entry.compare(e[parent], e[i]) <= 0:
                return
            self._swap(parent, i)
            i = parent

    def _sift_down(self, i: int) -> None:
        e = self._entries
        n = len(e)
        while True:
            left, right, smallest = 2 * i + 1, 2 * i + 2, i
            if left < n and Entry.compare(e[left], e[smallest]) < 0:
                smallest = left
            if right < n and Entry.compare(e[right], e[smallest]) < 0:
                smallest = right
            if smallest == i:
                return
            self._swap(i, smallest)
            i = smallest

    def _swap(self, a: int, b: int) -> None:
        """The one place the index map is maintained: every move of an entry updates its recorded position."""
        e = self._entries
        e[a], e[b] = e[b], e[a]
        self._index[e[a].key] = a
        self._index[e[b].key] = b

    def is_consistent(self) -> bool:
        """Both invariants: heap order and index consistency. For tests."""
        e = self._entries
        if not all(Entry.compare(e[(i - 1) // 2], e[i]) <= 0 for i in range(1, len(e))):
            return False
        if len(self._index) != len(e):
            return False
        return all(self._index.get(entry.key) == i for i, entry in enumerate(e))


@dataclass(frozen=True)
class RetryJob:
    """A retry due at a point in time: which payment, which attempt this will be."""

    payment_id: str
    attempt: int
    due_at_millis: int


class RetryScheduler:
    """Retry scheduler framing: the heap orders pending retries by due time, earliest first; ties (same
    millisecond) go in the order they were scheduled. Backoff is deterministic: base * 2**attempt, capped."""

    BASE_MILLIS = 1_000
    CAP_MILLIS = 60_000

    def __init__(self, clock: FakeClock) -> None:
        self._clock = clock
        self._heap: IndexedMinHeap[str] = IndexedMinHeap()
        self._jobs: dict[str, RetryJob] = {}

    @classmethod
    def backoff_millis(cls, attempt: int) -> int:
        if attempt < 0:
            raise ValueError("attempt must be >= 0")
        return min(cls.BASE_MILLIS << min(attempt, 20), cls.CAP_MILLIS)

    def schedule_retry(self, payment_id: str, attempt: int) -> int:
        """Schedule the next attempt for a payment after a failure. Returns the due time."""
        due_at = self._clock.now_millis() + self.backoff_millis(attempt)
        self._jobs[payment_id] = RetryJob(payment_id, attempt, due_at)
        self._heap.push(payment_id, due_at)
        return due_at

    def expedite(self, payment_id: str, new_due_at_millis: int) -> None:
        """Pull a retry forward (an operator or a webhook says "try now"). Never pushes it later."""
        self._heap.decrease_key(payment_id, new_due_at_millis)
        job = self._jobs[payment_id]
        self._jobs[payment_id] = RetryJob(payment_id, job.attempt, new_due_at_millis)

    def poll_due(self) -> list[RetryJob]:
        """Every job whose due time has arrived, earliest first. Removes them from the schedule."""
        due: list[RetryJob] = []
        now = self._clock.now_millis()
        while not self._heap.is_empty() and self._heap.peek().priority <= now:
            entry = self._heap.pop()
            due.append(self._jobs.pop(entry.key))
        return due

    def next_due_at_millis(self) -> int:
        return self._heap.peek().priority

    def pending(self) -> int:
        return self._heap.size()

    def is_scheduled(self, payment_id: str) -> bool:
        return self._heap.contains(payment_id)


def describe(jobs: list[RetryJob]) -> str:
    return "[" + ", ".join(f"{j.payment_id}(attempt {j.attempt})" for j in jobs) + "]"


def main() -> None:
    print("== heapify in O(n), then pop in sorted order ==")
    raw = [9, 4, 7, 1, 8, 2, 6, 3, 5]
    heap = MinHeap.heapify(raw, natural_order)
    print(f"input      = {raw}")
    popped = []
    while not heap.is_empty():
        popped.append(heap.pop())
    print(f"pop order  = {popped}")

    print("\n== 1,000 seeded random pushes and pops keep the heap property ==")
    rng = SeededRandom(42)
    stress: MinHeap[int] = MinHeap(natural_order)
    pushes = pops = 0
    for _ in range(1_000):
        if stress.is_empty() or rng.next_int(3) != 0:
            stress.push(rng.next_int(10_000))
            pushes += 1
        else:
            stress.pop()
            pops += 1
    print(f"pushes={pushes} pops={pops} size={stress.size()} isHeap={str(stress.is_heap()).lower()} min={stress.peek()}")

    print("\n== Retry scheduler: earliest due first, ties in scheduling order ==")
    clock = FakeClock(1_700_000_000_000)
    scheduler = RetryScheduler(clock)
    print(f"t+0ms: pay-A attempt 2 due at +{scheduler.schedule_retry('pay-A', 2) - clock.now_millis()}ms")
    print(f"t+0ms: pay-B attempt 0 due at +{scheduler.schedule_retry('pay-B', 0) - clock.now_millis()}ms")
    print(f"t+0ms: pay-C attempt 0 due at +{scheduler.schedule_retry('pay-C', 0) - clock.now_millis()}ms (same due time as pay-B)")
    print(f"t+0ms: pay-D attempt 6 due at +{scheduler.schedule_retry('pay-D', 6) - clock.now_millis()}ms (capped at 60s)")
    scheduler.expedite("pay-D", clock.now_millis() + 500)
    print("t+0ms: operator expedites pay-D to +500ms (decreaseKey)")
    print(f"next due in {scheduler.next_due_at_millis() - clock.now_millis()}ms, pending={scheduler.pending()}")

    clock.advance(1_000)
    print(f"t+1000ms: due = {describe(scheduler.poll_due())}")
    clock.advance(3_000)
    print(f"t+4000ms: due = {describe(scheduler.poll_due())}")
    print(f"pending={scheduler.pending()}")


if __name__ == "__main__":
    main()
