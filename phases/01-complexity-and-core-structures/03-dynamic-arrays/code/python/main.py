"""Lesson: phases/01-complexity-and-core-structures/03-dynamic-arrays/docs/en.md
Dynamic Arrays from Scratch: explicit capacity, a pluggable growth factor, and shrink hysteresis.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Cormen et al., Introduction to Algorithms, ch. 17 (amortized analysis of table doubling);
OpenJDK java.util.ArrayList.grow() (1.5x policy); Facebook folly FBVector.md (why 1.5x). Stdlib only.
"""

from __future__ import annotations

from enum import Enum
from typing import Generic, Iterator, Optional, TypeVar

T = TypeVar("T")


class GrowthPolicy(Enum):
    """How capacity grows when the backing array is full. The factor is the whole trade-off."""

    GROW_1_5X = "GROW_1_5X"
    GROW_2X = "GROW_2X"

    def next(self, capacity: int) -> int:
        """The capacity after one reallocation from `capacity`."""
        if self is GrowthPolicy.GROW_1_5X:
            # capacity + capacity/2, but always at least +1 so tiny arrays still grow (1 -> 2, 2 -> 3).
            return max(capacity + 1, capacity + (capacity >> 1))
        return max(capacity + 1, capacity << 1)

    def __str__(self) -> str:
        return self.value


class ArrayStats:
    """Counters that make the amortized argument measurable rather than believed."""

    def __init__(self) -> None:
        self.reallocations = 0     # how many times a new backing array was allocated
        self.elements_copied = 0   # total elements moved between backing arrays (grow + shrink)
        self.shrinks = 0           # how many of the reallocations were shrinks
        self.peak_capacity = 0     # largest backing array ever held

    def __str__(self) -> str:
        return (f"reallocations={self.reallocations} copied={self.elements_copied}"
                f" shrinks={self.shrinks} peakCapacity={self.peak_capacity}")


class DynamicArray(Generic[T]):
    """A growable array with explicit capacity, a pluggable growth factor, and a shrink policy with hysteresis.
    Invariant: 0 <= size <= capacity, elements[0..size) are live, elements[size..capacity) are None.
    Python's own list already hides a capacity; this class manages a fixed-length list of slots itself."""

    MIN_CAPACITY = 4

    def __init__(self, initial_capacity: int, policy: GrowthPolicy, shrink_enabled: bool = True) -> None:
        if initial_capacity < 1:
            raise ValueError("initial_capacity must be >= 1")
        if policy is None:
            raise ValueError("policy must not be None")
        self._elements: list[Optional[T]] = [None] * initial_capacity
        self._size = 0
        self._policy = policy
        self._shrink_enabled = shrink_enabled
        self._stats = ArrayStats()
        self._stats.peak_capacity = initial_capacity

    def size(self) -> int:
        return self._size

    def capacity(self) -> int:
        return len(self._elements)

    def is_empty(self) -> bool:
        return self._size == 0

    def stats(self) -> ArrayStats:
        return self._stats

    def _check_index(self, index: int) -> None:
        # size, not capacity: a slot past size is not an element. Negative indexes are rejected, not wrapped.
        if index < 0 or index >= self._size:
            raise IndexError(f"Index {index} out of bounds for length {self._size}")

    def add(self, value: T) -> None:
        """Append. O(1) amortized: a copy of all `size` elements happens only when size == capacity."""
        if self._size == len(self._elements):
            self._resize(self._policy.next(len(self._elements)))
        self._elements[self._size] = value
        self._size += 1

    def get(self, index: int) -> T:
        self._check_index(index)
        return self._elements[index]  # type: ignore[return-value]

    def set(self, index: int, value: T) -> T:
        self._check_index(index)
        old = self._elements[index]
        self._elements[index] = value
        return old  # type: ignore[return-value]

    def insert(self, index: int, value: T) -> None:
        """Insert at index, shifting the tail right. O(n - index): the cost the ring buffer lesson avoids."""
        if index < 0 or index > self._size:
            raise IndexError(f"index {index} size {self._size}")
        if self._size == len(self._elements):
            self._resize(self._policy.next(len(self._elements)))
        e = self._elements
        e[index + 1:self._size + 1] = e[index:self._size]  # System.arraycopy, shifting right by one
        e[index] = value
        self._size += 1

    def remove(self, index: int) -> T:
        """Remove at index, shifting the tail left, clearing the vacated slot so the GC can reclaim it."""
        self._check_index(index)
        e = self._elements
        old = e[index]
        if self._size - index - 1 > 0:
            e[index:self._size - 1] = e[index + 1:self._size]
        self._size -= 1
        e[self._size] = None
        self._maybe_shrink()
        return old  # type: ignore[return-value]

    def remove_last(self) -> T:
        if self._size == 0:
            raise IndexError("empty")  # Java: NoSuchElementException; Python's list.pop() raises IndexError
        return self.remove(self._size - 1)

    def clear(self) -> None:
        for i in range(self._size):
            self._elements[i] = None
        self._size = 0
        self._maybe_shrink()

    def _maybe_shrink(self) -> None:
        """Shrink only when less than a quarter full, and only to half capacity: the 1/4 trigger versus the
        1/2 post-shrink load is the hysteresis that stops an add/remove sequence at the boundary thrashing."""
        if not self._shrink_enabled:
            return
        capacity = len(self._elements)
        if capacity > self.MIN_CAPACITY and self._size * 4 < capacity:  # Python ints cannot overflow
            target = max(self.MIN_CAPACITY, capacity // 2)
            if target < capacity:
                self._stats.shrinks += 1
                self._resize(target)

    def _resize(self, new_capacity: int) -> None:
        if new_capacity < self._size:
            raise RuntimeError("cannot shrink below size")
        # Arrays.copyOf: a new backing list of new_capacity slots holding the live prefix.
        self._elements = self._elements[:self._size] + [None] * (new_capacity - self._size)
        self._stats.reallocations += 1
        self._stats.elements_copied += self._size
        if new_capacity > self._stats.peak_capacity:
            self._stats.peak_capacity = new_capacity

    @staticmethod
    def capacity_sequence(policy: GrowthPolicy, start: int, target: int) -> list[int]:
        """The capacity sequence a policy produces from `start`, until it first reaches `target`."""
        seq: DynamicArray[int] = DynamicArray(8, GrowthPolicy.GROW_2X, False)
        c = start
        seq.add(c)
        while c < target:
            c = policy.next(c)
            seq.add(c)
        return [seq.get(i) for i in range(seq.size())]

    def __iter__(self) -> Iterator[T]:
        cursor = 0
        while cursor < self._size:
            yield self.get(cursor)
            cursor += 1

    def __str__(self) -> str:
        live = ", ".join(str(self._elements[i]) for i in range(self._size))
        return f"[{live}] size={self._size} capacity={len(self._elements)}"


def measure_appends(policy: GrowthPolicy, n: int, initial_capacity: int) -> ArrayStats:
    """Append n values under a policy and return the counters."""
    array: DynamicArray[int] = DynamicArray(initial_capacity, policy, False)
    for i in range(n):
        array.add(i)
    return array.stats()


def _fmt(values: list[int]) -> str:
    return "[" + ", ".join(str(v) for v in values) + "]"


def main() -> None:
    print("== Capacity versus size ==")
    postings: DynamicArray[str] = DynamicArray(4, GrowthPolicy.GROW_2X)
    for p in ("debit:cash:1999", "credit:revenue:1941", "credit:fees:58"):
        postings.add(p)
        print(f"add {p} -> size={postings.size()} capacity={postings.capacity()}")
    postings.add("debit:cash:500")
    postings.add("credit:revenue:500")
    print(f"5th add forced a grow: {postings}")
    print()

    print("== Capacity sequences from 8 to 1,000,000 ==")
    for policy in GrowthPolicy:
        seq = DynamicArray.capacity_sequence(policy, 8, 1_000_000)
        print(f"{policy}: {len(seq) - 1} reallocations, final capacity {seq[-1]}")
        print(f"  first ten: {_fmt(seq[:10])}")
    print()

    print("== Measured copies for 1,000,000 appends (initial capacity 8) ==")
    n = 1_000_000
    for policy in GrowthPolicy:
        s = measure_appends(policy, n, 8)
        print("%s: %s  copies/append=%.3f  slack=%.1f%%" % (
            policy, s, s.elements_copied / n, 100.0 * (s.peak_capacity - n) / n))
    print("Both are O(1) amortized; the factor moves the constant between 'fewer copies' and 'less slack'.")
    print()

    print("== Can freed blocks be reused? (sum of earlier capacities vs the next one) ==")
    for policy in GrowthPolicy:
        seq = DynamicArray.capacity_sequence(policy, 8, 200)
        freed = 0
        parts = []
        for i in range(1, len(seq)):
            freed += seq[i - 1]
            parts.append(f"{seq[i]}{'(fits)' if freed >= seq[i] else '(no)'}")
        print(f"{policy}: {' '.join(parts)}")
    print()

    print("== Shrink with hysteresis: add/remove at the boundary does not thrash ==")
    queue: DynamicArray[int] = DynamicArray(4, GrowthPolicy.GROW_2X)
    for i in range(64):
        queue.add(i)
    print(f"after 64 adds: size={queue.size()} capacity={queue.capacity()} {queue.stats()}")
    while queue.size() > 16:
        queue.remove_last()
    print(f"down to 16:    size={queue.size()} capacity={queue.capacity()} {queue.stats()}")
    queue.remove_last()
    print(f"down to 15:    size={queue.size()} capacity={queue.capacity()} (15*4 < 64 -> shrink to 32)")
    before = queue.stats().reallocations
    for i in range(1000):
        queue.add(i)
        queue.remove_last()
    print(f"1000 add/remove pairs at the boundary: reallocations added = {queue.stats().reallocations - before}")


if __name__ == "__main__":
    main()
