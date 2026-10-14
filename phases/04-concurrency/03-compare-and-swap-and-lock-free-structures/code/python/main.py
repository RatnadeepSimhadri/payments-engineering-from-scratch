"""Lesson: phases/04-concurrency/03-compare-and-swap-and-lock-free-structures/docs/en.md
Compare-and-Swap and Lock-Free Structures: Treiber stack, the ABA problem, stamped references, Michael-Scott queue.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Treiber, IBM RJ 5118 (1986); Michael & Scott, PODC 1996; Herlihy & Shavit, The Art of Multiprocessor
Programming, ch. 3 and 11; java.util.concurrent.atomic Javadoc. CPython has no CAS: see AtomicReference below.
"""

from __future__ import annotations

import threading
import time
from collections import deque
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Generic, Optional, TypeVar

T = TypeVar("T")


# ---------------------------------------------------------------------------------------------------------------
# The atomics. CPython exposes no compare-and-swap instruction to Python code, so each atomic is a tiny object whose
# compare_and_set holds a private lock for the compare and the write. That makes the CAS itself blocking, but every
# algorithm below is written exactly as its lock-free original: read, compute, CAS, retry on failure. The ABA bug
# and its fix are about reference reuse, not about the hardware instruction, so they reproduce unchanged.
# ---------------------------------------------------------------------------------------------------------------

class AtomicReference(Generic[T]):
    """java.util.concurrent.atomic.AtomicReference: compare_and_set compares by identity (`is`), like Java's ==."""

    def __init__(self, value: Optional[T] = None) -> None:
        self._value = value
        self._lock = threading.Lock()

    def get(self) -> Optional[T]:
        return self._value  # a single attribute read is atomic in CPython

    def set(self, value: Optional[T]) -> None:
        with self._lock:
            self._value = value

    def compare_and_set(self, expected: Optional[T], new: Optional[T]) -> bool:
        with self._lock:
            if self._value is expected:
                self._value = new
                return True
            return False


class AtomicLong:
    """java.util.concurrent.atomic.AtomicLong, reduced to what the lesson uses."""

    def __init__(self, value: int = 0) -> None:
        self._value = value
        self._lock = threading.Lock()

    def get(self) -> int:
        return self._value

    def increment_and_get(self) -> int:
        with self._lock:  # += is a read, an add, and a write: three bytecodes a thread switch can split
            self._value += 1
            return self._value


class AtomicStampedReference(Generic[T]):
    """A (reference, stamp) pair compared and swapped as one unit: what a double-word CAS provides in hardware."""

    def __init__(self, reference: Optional[T], stamp: int) -> None:
        self._pair = (reference, stamp)
        self._lock = threading.Lock()

    def get(self) -> tuple[Optional[T], int]:
        """Java's get(int[] stampHolder): returns the reference and the stamp read together."""
        return self._pair

    def get_reference(self) -> Optional[T]:
        return self._pair[0]

    def compare_and_set(self, expected_ref: Optional[T], new_ref: Optional[T], expected_stamp: int,
                        new_stamp: int) -> bool:
        with self._lock:
            ref, stamp = self._pair
            if ref is expected_ref and stamp == expected_stamp:
                self._pair = (new_ref, new_stamp)
                return True
            return False


def on_spin_wait() -> None:
    """Thread.onSpinWait: under the GIL a spinning thread must give up the interpreter or it starves the producer."""
    time.sleep(0)


class TreiberStack(Generic[T]):
    """Treiber's lock-free stack. Every push and pop is one CAS on head; a failed CAS means another thread
    published first, so the loop re-reads and tries again. Nodes are freshly allocated and never reused, which
    is what makes this version immune to ABA under a garbage collector."""

    class Node:
        __slots__ = ("value", "next")

        def __init__(self, value) -> None:
            self.value = value
            self.next: Optional[TreiberStack.Node] = None

    def __init__(self) -> None:
        self._head: AtomicReference[TreiberStack.Node] = AtomicReference()
        self._cas_retries = AtomicLong()

    def push(self, value: T) -> None:
        if value is None:
            raise TypeError("value")  # Java: NullPointerException from Objects.requireNonNull
        node = TreiberStack.Node(value)
        while True:
            current = self._head.get()
            node.next = current  # link before publishing: next is set, then CAS publishes the node
            if self._head.compare_and_set(current, node):
                return
            self._cas_retries.increment_and_get()

    def pop(self) -> Optional[T]:
        """The top value, or None when empty (Java returns Optional.empty())."""
        while True:
            current = self._head.get()
            if current is None:
                return None
            nxt = current.next
            if self._head.compare_and_set(current, nxt):
                return current.value
            self._cas_retries.increment_and_get()

    def peek(self) -> Optional[T]:
        current = self._head.get()
        return None if current is None else current.value

    def is_empty(self) -> bool:
        return self._head.get() is None

    def size(self) -> int:
        """O(n) snapshot for tests and demos; not linearizable with respect to concurrent pushes."""
        n = 0
        cur = self._head.get()
        while cur is not None:
            n += 1
            cur = cur.next
        return n

    def cas_retries(self) -> int:
        return self._cas_retries.get()


@dataclass(frozen=True)
class CaptureJob:
    """A capture job popped from a work stack. The payments payload for the ABA demonstration."""

    payment_id: str
    amount_minor: int


class PooledNode:
    """A recyclable node with a stable id so the demo can print "N3" and show the same node reappearing."""

    __slots__ = ("id", "value", "next")

    def __init__(self, id: int) -> None:
        self.id = id
        self.value: Optional[CaptureJob] = None
        self.next: Optional[PooledNode] = None


class PooledStack:
    """A Treiber stack that recycles its nodes through a free list. Recycling re-enables the ABA problem: a popped
    node can be pushed again, so head can hold the same reference with different contents.
    before_cas is a test seam that runs between the read of head and the CAS in pop."""

    Node = PooledNode

    def __init__(self, before_cas: Callable[[], None] = lambda: None) -> None:
        self._head: AtomicReference[PooledNode] = AtomicReference()
        self._free_list: deque[PooledNode] = deque()  # FIFO: the oldest freed node is reused first
        self._before_cas = before_cas
        self._next_id = 1
        self._pool_lock = threading.Lock()  # Java: synchronized allocate/free

    def _allocate(self, value: CaptureJob) -> PooledNode:
        with self._pool_lock:
            if self._free_list:
                node = self._free_list.popleft()
            else:
                node = PooledNode(self._next_id)
                self._next_id += 1
            node.value = value
            return node

    def _free(self, node: PooledNode) -> None:
        with self._pool_lock:
            self._free_list.append(node)

    def push(self, value: CaptureJob) -> None:
        node = self._allocate(value)
        while True:
            current = self._head.get()
            node.next = current
            if self._head.compare_and_set(current, node):
                return

    def pop(self) -> Optional[CaptureJob]:
        while True:
            current = self._head.get()
            if current is None:
                return None
            nxt = current.next
            self._before_cas()  # the gap where ABA happens
            if self._head.compare_and_set(current, nxt):
                value = current.value
                self._free(current)
                return value

    def head_node(self) -> Optional[PooledNode]:
        return self._head.get()

    def snapshot(self) -> list[str]:
        out = []
        cur = self._head.get()
        while cur is not None:
            out.append(f"N{cur.id}={cur.value.payment_id}")
            cur = cur.next
        return out


class StampedPooledStack:
    """The pooled stack with the fix: head is an AtomicStampedReference. Every successful CAS increments the stamp,
    so a CAS expecting (N3, stamp 3) fails once the stack has moved on to (N3, stamp 6)."""

    def __init__(self, before_cas: Callable[[], None] = lambda: None) -> None:
        self._head: AtomicStampedReference[PooledNode] = AtomicStampedReference(None, 0)
        self._free_list: deque[PooledNode] = deque()
        self._before_cas = before_cas
        self._stale_cas_rejected = AtomicLong()
        self._next_id = 1
        self._pool_lock = threading.Lock()

    def _allocate(self, value: CaptureJob) -> PooledNode:
        with self._pool_lock:
            if self._free_list:
                node = self._free_list.popleft()
            else:
                node = PooledNode(self._next_id)
                self._next_id += 1
            node.value = value
            return node

    def _free(self, node: PooledNode) -> None:
        with self._pool_lock:
            self._free_list.append(node)

    def push(self, value: CaptureJob) -> None:
        node = self._allocate(value)
        while True:
            current, stamp = self._head.get()
            node.next = current
            if self._head.compare_and_set(current, node, stamp, stamp + 1):
                return

    def pop(self) -> Optional[CaptureJob]:
        while True:
            current, stamp = self._head.get()
            if current is None:
                return None
            nxt = current.next
            self._before_cas()
            if self._head.compare_and_set(current, nxt, stamp, stamp + 1):
                value = current.value
                self._free(current)
                return value
            self._stale_cas_rejected.increment_and_get()

    def stamp(self) -> int:
        return self._head.get()[1]

    def stale_cas_rejected(self) -> int:
        return self._stale_cas_rejected.get()

    def snapshot(self) -> list[str]:
        out = []
        cur = self._head.get_reference()
        while cur is not None:
            out.append(f"N{cur.id}={cur.value.payment_id}")
            cur = cur.next
        return out


class MichaelScottQueue(Generic[T]):
    """Michael-Scott lock-free FIFO queue. head is a sentinel whose next is the first real element; tail is the
    last node or one behind it. Enqueue links with a CAS on tail.next, then swings tail; a thread that finds tail
    lagging helps by swinging it before retrying."""

    class Node:
        __slots__ = ("value", "next")

        def __init__(self, value) -> None:
            self.value = value
            self.next: AtomicReference[MichaelScottQueue.Node] = AtomicReference()

    def __init__(self) -> None:
        sentinel = MichaelScottQueue.Node(None)
        self._head: AtomicReference[MichaelScottQueue.Node] = AtomicReference(sentinel)
        self._tail: AtomicReference[MichaelScottQueue.Node] = AtomicReference(sentinel)
        self._cas_retries = AtomicLong()
        self._helped_tail = AtomicLong()

    def enqueue(self, value: T) -> None:
        if value is None:
            raise TypeError("value")
        node = MichaelScottQueue.Node(value)
        while True:
            last = self._tail.get()
            nxt = last.next.get()
            if last is not self._tail.get():  # tail moved under us; re-read
                self._cas_retries.increment_and_get()
                continue
            if nxt is None:
                if last.next.compare_and_set(None, node):  # step 1: link the node (linearization point)
                    self._tail.compare_and_set(last, node)  # step 2: swing tail; failure is fine, someone helped
                    return
                self._cas_retries.increment_and_get()
            else:
                self._helped_tail.increment_and_get()
                self._tail.compare_and_set(last, nxt)  # tail is lagging: help, then retry

    def dequeue(self) -> Optional[T]:
        while True:
            first = self._head.get()
            last = self._tail.get()
            nxt = first.next.get()
            if first is not self._head.get():
                self._cas_retries.increment_and_get()
                continue
            if nxt is None:
                return None  # sentinel has no successor: empty
            if first is last:  # tail lagging behind a real node: help
                self._helped_tail.increment_and_get()
                self._tail.compare_and_set(last, nxt)
                continue
            value = nxt.value  # read before the CAS: after it, nxt is the new sentinel
            if self._head.compare_and_set(first, nxt):
                return value
            self._cas_retries.increment_and_get()

    def is_empty(self) -> bool:
        return self._head.get().next.get() is None

    def cas_retries(self) -> int:
        return self._cas_retries.get()

    def helped_tail(self) -> int:
        return self._helped_tail.get()


class Progress(Enum):
    """The four standard progress guarantees, from weakest to strongest."""

    BLOCKING = "a thread holding a lock that is descheduled blocks every other thread"
    OBSTRUCTION_FREE = "a thread running alone for long enough completes; contention may livelock"
    LOCK_FREE = "some thread completes in a bounded number of steps; an individual thread may starve"
    WAIT_FREE = "every thread completes in a bounded number of its own steps"

    @property
    def meaning(self) -> str:
        return self.value

    @property
    def ordinal(self) -> int:
        return list(Progress).index(self)


def stress_stack(stack: TreiberStack[int], threads: int, per_thread: int) -> int:
    """Runs `threads` workers that each perform `per_thread` push/pop pairs; returns the elapsed millis."""
    start = threading.Event()  # CountDownLatch(1)
    popped = AtomicLong()

    def work(base: int) -> None:
        start.wait()
        for i in range(per_thread):
            stack.push(base + i)
            if stack.pop() is not None:
                popped.increment_and_get()

    workers = [threading.Thread(target=work, args=(t * per_thread,)) for t in range(threads)]
    for w in workers:
        w.start()
    t0 = time.perf_counter_ns()
    start.set()
    for w in workers:  # CountDownLatch(threads).await()
        w.join()
    return (time.perf_counter_ns() - t0) // 1_000_000


def demo_treiber() -> None:
    print("== A lock-free stack: one CAS per push, one per pop ==")
    stack: TreiberStack[str] = TreiberStack()
    stack.push("pay_001")
    stack.push("pay_002")
    stack.push("pay_003")
    print(f"pushed pay_001, pay_002, pay_003; size = {stack.size()}")
    a = stack.pop()
    b = stack.pop()
    print(f"pop -> {a or 'empty'}, pop -> {b or 'empty'}")
    print(f"peek -> {stack.peek() or 'empty'}, size = {stack.size()}")
    print(f"pop on empty -> {TreiberStack().pop()}")
    print()


def demo_aba() -> None:
    print("== ABA: the same node, a different stack ==")
    holder: list[PooledStack] = []
    armed = [True]

    def thread_b() -> None:
        if not armed[0]:
            return
        armed[0] = False  # only the first pop is interrupted
        s = holder[0]
        print("  thread A paused between reading head=N3 and its CAS")
        job = s.pop()
        print(f"  thread B: pop -> {job.payment_id if job else 'empty'}  (N3 goes to the free list)")
        job = s.pop()
        print(f"  thread B: pop -> {job.payment_id if job else 'empty'}  (N2 goes to the free list)")
        s.push(CaptureJob("pay_004", 4_000))
        print(f"  thread B: push pay_004 reuses N3; stack is now [{', '.join(s.snapshot())}]")

    stack = PooledStack(thread_b)
    holder.append(stack)
    stack.push(CaptureJob("pay_001", 1_000))
    stack.push(CaptureJob("pay_002", 2_000))
    stack.push(CaptureJob("pay_003", 3_000))
    print(f"before: [{', '.join(stack.snapshot())}]")
    got = stack.pop()
    print(f"thread A resumes: CAS(head, N3 -> N2) succeeds, returns {got.payment_id}")
    print(f"after:  [{', '.join(stack.snapshot())}]   <- pay_002 is back on the stack and will be captured twice")
    print()


def demo_stamped() -> None:
    print("== The fix: a stamp that changes on every CAS ==")
    holder: list[StampedPooledStack] = []
    armed = [True]

    def thread_b() -> None:
        if not armed[0]:
            return
        armed[0] = False
        s = holder[0]
        s.pop()
        s.pop()
        s.push(CaptureJob("pay_004", 4_000))
        print(f"  thread B: pop, pop, push pay_004 -> stamp is now {s.stamp()}, stack [{', '.join(s.snapshot())}]")

    stack = StampedPooledStack(thread_b)
    holder.append(stack)
    stack.push(CaptureJob("pay_001", 1_000))
    stack.push(CaptureJob("pay_002", 2_000))
    stack.push(CaptureJob("pay_003", 3_000))
    print(f"before: [{', '.join(stack.snapshot())}] stamp = {stack.stamp()}")
    got = stack.pop()
    print(f"thread A: CAS expecting (N3, stamp 3) rejected ({stack.stale_cas_rejected()}"
          f" stale CAS), retried, returned {got.payment_id}")
    print(f"after:  [{', '.join(stack.snapshot())}]   <- every job captured exactly once")
    print()


def demo_queue() -> None:
    print("== Michael-Scott queue: two CAS pointers and a helping step ==")
    queue: MichaelScottQueue[int] = MichaelScottQueue()
    for i in range(1, 4):
        queue.enqueue(i)
    d1, d2, d3, d4 = queue.dequeue(), queue.dequeue(), queue.dequeue(), queue.dequeue()
    print(f"enqueue 1,2,3; dequeue -> {d1}, {d2}, {d3}, then {'empty' if d4 is None else d4}")

    producers, per_producer = 4, 20_000

    def produce(base: int) -> None:
        for i in range(per_producer):
            queue.enqueue(base + i)

    workers = [threading.Thread(target=produce, args=(p * per_producer,)) for p in range(producers)]
    for w in workers:
        w.start()
    for w in workers:
        w.join()
    seen = [False] * (producers * per_producer)
    count = 0
    v = queue.dequeue()
    while v is not None:
        seen[v] = True
        count += 1
        v = queue.dequeue()
    print(f"{producers} producers x {per_producer} enqueues -> dequeued {count}, "
          f"every value exactly once: {'true' if all(seen) else 'false'}")
    print()


def demo_progress() -> None:
    print("== Progress guarantees ==")
    for p in Progress:
        print("%-17s %s" % (p.name, p.meaning))
    print("Treiber stack and Michael-Scott queue: LOCK_FREE (a CAS fails only because another thread's succeeded).")
    print()


def main() -> None:
    demo_treiber()
    demo_aba()
    demo_stamped()
    demo_queue()

    print("== Stress: 4 threads x 50,000 push/pop pairs on one Treiber stack ==")
    stack: TreiberStack[int] = TreiberStack()
    stress_stack(stack, 4, 50_000)
    print(f"stack empty afterwards: {'true' if stack.is_empty() else 'false'}; "
          "every push matched by a pop, nothing lost, nothing duplicated")
    print("(CAS retries are counted by stack.cas_retries(); the number depends on the scheduler, which is the point)")
    print()

    demo_progress()


if __name__ == "__main__":
    main()
