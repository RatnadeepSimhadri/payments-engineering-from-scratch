"""Lesson: phases/01-complexity-and-core-structures/05-stacks-queues-and-deques/docs/en.md
Stacks, Queues, and Ring Buffers.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Knuth, TAOCP Vol. 1, 2.2.1-2.2.2 (stacks, queues, deques; circular buffers); LMAX Disruptor
technical paper (2011) for sequence-numbered ring buffers; Apache Kafka docs (offsets). Stdlib only.
"""

from __future__ import annotations

from enum import Enum
from typing import Generic, Iterator, Optional, TypeVar

T = TypeVar("T")


class ArrayStack(Generic[T]):
    """LIFO stack over a growable array. push/pop/peek are O(1) amortized; the top is always at index size-1."""

    def __init__(self, initial_capacity: int) -> None:
        if initial_capacity < 1:
            raise ValueError("initial_capacity must be >= 1")
        self._elements: list[Optional[T]] = [None] * initial_capacity
        self._size = 0

    def size(self) -> int:
        return self._size

    def is_empty(self) -> bool:
        return self._size == 0

    def capacity(self) -> int:
        return len(self._elements)

    def push(self, value: T) -> None:
        if self._size == len(self._elements):
            self._elements = self._elements + [None] * len(self._elements)  # Arrays.copyOf to double
        self._elements[self._size] = value
        self._size += 1

    def pop(self) -> T:
        if self._size == 0:
            raise IndexError("stack is empty")  # Java: NoSuchElementException
        self._size -= 1
        v = self._elements[self._size]
        self._elements[self._size] = None  # do not pin the popped object
        return v  # type: ignore[return-value]

    def peek(self) -> T:
        if self._size == 0:
            raise IndexError("stack is empty")
        return self._elements[self._size - 1]  # type: ignore[return-value]

    def __iter__(self) -> Iterator[T]:
        """Iterates from the top down: the order pop() would return."""
        cursor = self._size - 1
        while cursor >= 0:
            yield self._elements[cursor]  # type: ignore[misc]
            cursor -= 1


class OverflowPolicy(Enum):
    """What a bounded buffer does when a producer offers into a full buffer. BLOCK belongs to Phase 4."""

    REJECT = "REJECT"            # offer returns False; the producer must slow down or retry: backpressure
    DROP_OLDEST = "DROP_OLDEST"  # overwrite the oldest element; the consumer loses history, the producer never waits


class RingBuffer(Generic[T]):
    """Bounded double-ended queue over a fixed array, addressed by monotonically increasing sequence numbers.
    head is the sequence of the oldest element, tail the sequence the next offer_last will take; size = tail - head.
    A sequence maps to a slot with seq % capacity, so the buffer wraps around without ever moving an element."""

    def __init__(self, capacity: int, policy: OverflowPolicy) -> None:
        if capacity < 1:
            raise ValueError("capacity must be >= 1")
        if policy is None:
            raise ValueError("policy must not be None")
        self._slots: list[Optional[T]] = [None] * capacity
        self._policy = policy
        self._head = 0      # sequence of the first (oldest) element
        self._tail = 0      # sequence one past the last (newest) element
        self._dropped = 0   # elements overwritten under DROP_OLDEST

    def capacity(self) -> int:
        return len(self._slots)

    def size(self) -> int:
        return self._tail - self._head

    def is_empty(self) -> bool:
        return self._head == self._tail

    def is_full(self) -> bool:
        return self._tail - self._head == len(self._slots)

    def head_sequence(self) -> int:
        return self._head

    def tail_sequence(self) -> int:
        return self._tail

    def dropped(self) -> int:
        return self._dropped

    def index_of(self, seq: int) -> int:
        """The slot for a sequence number. With a power-of-two capacity this could be seq & (capacity - 1)."""
        return seq % len(self._slots)

    def offer_last(self, value: T) -> bool:
        """Enqueue at the back. Returns False only under REJECT when full. O(1), no element ever moves."""
        if self.is_full():
            if self._policy is OverflowPolicy.REJECT:
                return False
            self._slots[self.index_of(self._head)] = None  # DROP_OLDEST: the slot tail is about to reuse
            self._head += 1
            self._dropped += 1
        self._slots[self.index_of(self._tail)] = value
        self._tail += 1
        return True

    def offer_first(self, value: T) -> bool:
        """Enqueue at the front, stealing the slot before head. Under DROP_OLDEST when full, drops the newest."""
        if self.is_full():
            if self._policy is OverflowPolicy.REJECT:
                return False
            self._tail -= 1
            self._slots[self.index_of(self._tail)] = None
            self._dropped += 1
        self._head -= 1
        if self._head < 0:
            self._rebase()
        self._slots[self.index_of(self._head)] = value
        return True

    def _rebase(self) -> None:
        """Keep sequences non-negative so offsets match the Java and Go versions exactly."""
        self._head += len(self._slots)
        self._tail += len(self._slots)

    def poll_first(self) -> Optional[T]:
        if self.is_empty():
            return None
        i = self.index_of(self._head)
        v = self._slots[i]
        self._slots[i] = None
        self._head += 1
        return v

    def poll_last(self) -> Optional[T]:
        if self.is_empty():
            return None
        self._tail -= 1
        i = self.index_of(self._tail)
        v = self._slots[i]
        self._slots[i] = None
        return v

    def peek_first(self) -> Optional[T]:
        return None if self.is_empty() else self._slots[self.index_of(self._head)]

    def peek_last(self) -> Optional[T]:
        return None if self.is_empty() else self._slots[self.index_of(self._tail - 1)]

    def get(self, i: int) -> T:
        """O(1) random access to the i-th element from the front: the thing a linked deque cannot do."""
        if i < 0 or i >= self.size():
            raise IndexError(f"Index {i} out of bounds for length {self.size()}")
        return self._slots[self.index_of(self._head + i)]  # type: ignore[return-value]

    def clear(self) -> None:
        for i in range(len(self._slots)):
            self._slots[i] = None
        self._head = self._tail = 0

    def __iter__(self) -> Iterator[T]:
        seq = self._head
        while seq < self._tail:
            yield self._slots[self.index_of(seq)]  # type: ignore[misc]
            seq += 1

    def slots_view(self) -> str:
        """Debug view of the raw slots, in physical order, with the head and tail positions marked."""
        parts = []
        for i, slot in enumerate(self._slots):
            mark = ""
            if not self.is_empty() and i == self.index_of(self._head):
                mark += "H"
            if i == self.index_of(self._tail) and not self.is_full():
                mark += "T"
            if self.is_full() and i == self.index_of(self._head):
                mark += "T"
            parts.append(f"[{'.' if slot is None else slot}]{mark}")
        return " ".join(parts)

    def __str__(self) -> str:
        live = ", ".join(str(self._slots[self.index_of(s)]) for s in range(self._head, self._tail))
        return f"[{live}] head={self._head} tail={self._tail} size={self.size()}"


def brackets_balanced(text: str) -> bool:
    """Classic stack use: check that brackets in a message body are balanced. O(n)."""
    open_: ArrayStack[str] = ArrayStack(8)
    for c in text:
        if c in "([{":
            open_.push(c)
        elif c in ")]}":
            if open_.is_empty():
                return False
            o = open_.pop()
            if (c == ")" and o != "(") or (c == "]" and o != "[") or (c == "}" and o != "{"):
                return False
    return open_.is_empty()


def _b(value: bool) -> str:
    return "true" if value else "false"


def main() -> None:
    print("== Stack: compensating a saga in reverse order ==")
    compensations: ArrayStack[str] = ArrayStack(2)
    steps = ["reserve funds on wallet", "post ledger hold", "request capture at acquirer"]
    undo = ["release wallet reservation", "reverse ledger hold", "void acquirer authorization"]
    for i in range(len(steps)):
        print(f"step {i + 1}: {steps[i]}  -> push undo: {undo[i]}")
        compensations.push(undo[i])
    print(f"step 4 failed; unwinding {compensations.size()} compensations "
          f"(capacity grew to {compensations.capacity()}):")
    while not compensations.is_empty():
        print(f"  pop -> {compensations.pop()}")
    print('brackets balanced in {"a":[1,2,{"b":3}]} ? ' + _b(brackets_balanced('{"a":[1,2,{"b":3}]}')))
    print('brackets balanced in {"a":[1,2}   ? ' + _b(brackets_balanced('{"a":[1,2}')))
    print()

    print("== Ring buffer: wraparound with capacity 4 (sequence -> slot = seq % 4) ==")
    ring: RingBuffer[str] = RingBuffer(4, OverflowPolicy.REJECT)
    for e in ("e0", "e1", "e2", "e3"):
        ring.offer_last(e)
    print(f"offer e0..e3   : {ring.slots_view()}   {ring}")
    print(f"offer e4 (full): accepted={_b(ring.offer_last('e4'))}  (REJECT: producer must back off)")
    first = ring.poll_first()
    second = ring.poll_first()
    print(f"poll -> {first}, poll -> {second}")
    print(f"after 2 polls  : {ring.slots_view()}   {ring}")
    ring.offer_last("e4")
    ring.offer_last("e5")
    print(f"offer e4, e5   : {ring.slots_view()}   {ring}")
    print(f"e5 has sequence 5 and lives in slot 5 % 4 = {ring.index_of(5)}; logical order is still {ring}")
    print(f"get(0)={ring.get(0)} get(3)={ring.get(3)}  (O(1) by index, through the wrap)")
    print()

    print("== Deque: both ends in O(1) ==")
    deque: RingBuffer[int] = RingBuffer(5, OverflowPolicy.REJECT)
    deque.offer_last(2)
    deque.offer_last(3)
    deque.offer_first(1)
    deque.offer_first(0)
    print(f"offerLast 2,3 then offerFirst 1,0 -> {deque}")
    last = deque.poll_last()
    head = deque.poll_first()
    print(f"pollLast -> {last}, pollFirst -> {head}, now {deque}")
    print()

    print("== Bounded queue semantics: a slow consumer under two policies ==")
    rejecting: RingBuffer[str] = RingBuffer(4, OverflowPolicy.REJECT)
    dropping: RingBuffer[str] = RingBuffer(4, OverflowPolicy.DROP_OLDEST)
    rejected = 0
    for i in range(10):
        event = f"payment.{i}"
        if not rejecting.offer_last(event):
            rejected += 1
        dropping.offer_last(event)
    print("10 events offered, consumer read none:")
    print(f"  REJECT      : holds {rejecting}; rejected={rejected} (the producer saw every rejection)")
    print(f"  DROP_OLDEST : holds {dropping}; dropped={dropping.dropped()} "
          f"(the consumer resumes at offset {dropping.head_sequence()})")
    print()

    print("== Broker framing: sequences are offsets ==")
    partition: RingBuffer[str] = RingBuffer(8, OverflowPolicy.DROP_OLDEST)
    for i in range(12):
        partition.offer_last(f"msg{i}")
    print(f"12 messages into a capacity-8 log: retained offsets {partition.head_sequence()}.."
          f"{partition.tail_sequence() - 1}, evicted {partition.dropped()}")
    consumer_offset = 2
    print(f"a consumer at offset {consumer_offset} is behind the retention window: it must reset to "
          f"{partition.head_sequence()} and has lost {partition.head_sequence() - consumer_offset} messages")
    consumer_offset = 9
    print(f"a consumer at offset {consumer_offset} reads {partition.get(consumer_offset - partition.head_sequence())}"
          f" next; {partition.tail_sequence() - consumer_offset} messages of lag")


if __name__ == "__main__":
    main()
