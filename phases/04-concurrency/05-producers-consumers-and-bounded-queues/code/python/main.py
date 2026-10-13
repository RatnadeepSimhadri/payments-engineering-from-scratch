"""Lesson: phases/04-concurrency/05-producers-consumers-and-bounded-queues/docs/en.md
Producers, Consumers, and Bounded Queues
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: threading.Condition docs (wait(timeout), spurious wakeups); Hoare, "Monitors: An Operating System
Structuring Concept" (1974); CPython's queue.Queue source (three-condition design).
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import threading
import time
from dataclasses import dataclass
from typing import Generic, Protocol, TypeVar

T = TypeVar("T")


# ---------------------------------------------------------------------------
# Money (copied from Phase 10, Lesson 01)
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3:
            raise ValueError(f"currency code must be three letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """Exact amount in minor units of one currency; immutable, so safe to hand from thread to thread."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        return Money(self.minor_units + other.minor_units, self.currency)

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


@dataclass(frozen=True)
class Payment:
    """The unit of work flowing through the queue: an authorization waiting to be posted to the ledger."""

    id: str
    amount: Money


# ---------------------------------------------------------------------------
# Time, injected
# ---------------------------------------------------------------------------


class Clock(Protocol):
    def nanos(self) -> int: ...
    def wait(self, condition: threading.Condition, nanos: int) -> None: ...


class SystemClock:
    def nanos(self) -> int:
        return time.monotonic_ns()

    def wait(self, condition: threading.Condition, nanos: int) -> None:
        condition.wait(nanos / 1_000_000_000)


class FakeClock:
    """No real waiting: the clock jumps to the deadline, as if the wait timed out unsignalled."""

    def __init__(self, start_nanos: int = 0) -> None:
        self._now = start_nanos

    def nanos(self) -> int:
        return self._now

    def wait(self, condition: threading.Condition, nanos: int) -> None:
        self._now += max(0, nanos)


@dataclass(frozen=True)
class QueueStats:
    """Counters the queue keeps under its lock. Wait counts are events; waiting counts are current."""

    producer_waits: int
    consumer_waits: int
    high_water_mark: int
    waiting_producers: int
    waiting_consumers: int


# ---------------------------------------------------------------------------
# The queue
# ---------------------------------------------------------------------------


class BoundedBlockingQueue(Generic[T]):
    """A FIFO ring buffer with a fixed capacity, built from one lock and two condition variables.
    put blocks while full, take blocks while empty; offer and poll give up after a timeout.
    The bound is the point: a producer that outruns the consumer is slowed down, not buffered forever."""

    def __init__(self, capacity: int, clock: Clock | None = None) -> None:
        if capacity <= 0:
            raise ValueError(f"capacity must be positive: {capacity}")
        self._items: list[T | None] = [None] * capacity
        self._head = 0  # next take
        self._tail = 0  # next put
        self._count = 0
        self._lock = threading.Lock()
        self._not_full = threading.Condition(self._lock)
        self._not_empty = threading.Condition(self._lock)
        self._clock: Clock = clock or SystemClock()
        self._producer_waits = 0
        self._consumer_waits = 0
        self._high_water_mark = 0
        self._waiting_producers = 0
        self._waiting_consumers = 0

    def capacity(self) -> int:
        return len(self._items)

    def size(self) -> int:
        with self._lock:
            return self._count

    def is_empty(self) -> bool:
        return self.size() == 0

    def is_full(self) -> bool:
        return self.size() == len(self._items)

    def stats(self) -> QueueStats:
        with self._lock:
            return QueueStats(self._producer_waits, self._consumer_waits, self._high_water_mark,
                              self._waiting_producers, self._waiting_consumers)

    def put(self, item: T) -> None:
        """Blocks while the queue is full. This is backpressure: the caller's thread is the buffer."""
        if item is None:
            raise ValueError("item must not be None")
        with self._lock:
            while self._count == len(self._items):   # while, not if: wakeups can be spurious or stolen
                self._producer_waits += 1
                self._waiting_producers += 1
                try:
                    self._not_full.wait()
                finally:
                    self._waiting_producers -= 1
            self._enqueue(item)

    def take(self) -> T:
        """Blocks while the queue is empty."""
        with self._lock:
            while self._count == 0:
                self._consumer_waits += 1
                self._waiting_consumers += 1
                try:
                    self._not_empty.wait()
                finally:
                    self._waiting_consumers -= 1
            return self._dequeue()

    def offer(self, item: T, timeout_nanos: int) -> bool:
        """Waits at most timeout_nanos for space; False means shed the load: tell the caller to retry later."""
        if item is None:
            raise ValueError("item must not be None")
        with self._lock:
            deadline = self._clock.nanos() + timeout_nanos
            while self._count == len(self._items):
                remaining = deadline - self._clock.nanos()
                if remaining <= 0:
                    return False
                self._producer_waits += 1
                self._waiting_producers += 1
                try:
                    self._clock.wait(self._not_full, remaining)
                finally:
                    self._waiting_producers -= 1
            self._enqueue(item)
            return True

    def poll(self, timeout_nanos: int) -> T | None:
        """Waits at most timeout_nanos for an item; None means the consumer can do housekeeping and come back."""
        with self._lock:
            deadline = self._clock.nanos() + timeout_nanos
            while self._count == 0:
                remaining = deadline - self._clock.nanos()
                if remaining <= 0:
                    return None
                self._consumer_waits += 1
                self._waiting_consumers += 1
                try:
                    self._clock.wait(self._not_empty, remaining)
                finally:
                    self._waiting_consumers -= 1
            return self._dequeue()

    # Both helpers assume the lock is held and the precondition (space / an item) was checked.
    def _enqueue(self, item: T) -> None:
        self._items[self._tail] = item
        self._tail = (self._tail + 1) % len(self._items)
        self._count += 1
        self._high_water_mark = max(self._high_water_mark, self._count)
        self._not_empty.notify()                     # one waiting consumer is enough: one item arrived

    def _dequeue(self) -> T:
        item = self._items[self._head]
        self._items[self._head] = None               # let the GC have it
        self._head = (self._head + 1) % len(self._items)
        self._count -= 1
        self._not_full.notify()                      # one waiting producer is enough: one slot opened
        assert item is not None
        return item


class Producer(threading.Thread):
    """A producer that puts a scripted list of payments."""

    def __init__(self, queue: BoundedBlockingQueue[Payment], script: list[Payment], done: threading.Event) -> None:
        super().__init__(name="producer", daemon=True)
        self._queue = queue
        self._script = script
        self._done = done

    def run(self) -> None:
        try:
            for p in self._script:
                self._queue.put(p)
        finally:
            self._done.set()

    @staticmethod
    def script(prefix: str, n: int, minor_units_each: int) -> list[Payment]:
        return [Payment(f"{prefix}-{i}", Money.of_minor(minor_units_each, USD)) for i in range(n)]


def until_producer_waiting(queue: BoundedBlockingQueue) -> None:
    """Yield (never sleep for a duration) until the queue reports a waiting producer; bounded so a bug cannot hang."""
    deadline = time.monotonic_ns() + 5_000_000_000
    while queue.stats().waiting_producers == 0:
        if time.monotonic_ns() > deadline:
            raise RuntimeError("producer never blocked")
        time.sleep(0)  # give up the GIL; this is a yield, not a delay


def main() -> None:
    print("== A full queue refuses, it does not grow ==")
    ingest: BoundedBlockingQueue[Payment] = BoundedBlockingQueue(3)
    for i in range(1, 6):
        p = Payment(f"auth-{i}", Money.of_minor(1_999, USD))
        accepted = ingest.offer(p, 0)
        verdict = "accepted" if accepted else "REFUSED: reply 429 Retry-After"
        print(f"offer {p.id} -> {verdict}  (size {ingest.size()}/{ingest.capacity()})")

    print("\n== A timed offer against a full queue, with a fake clock ==")
    clock = FakeClock(0)
    timed: BoundedBlockingQueue[Payment] = BoundedBlockingQueue(1, clock)
    timed.put(Payment("auth-a", Money.of_minor(100, USD)))
    accepted = timed.offer(Payment("auth-b", Money.of_minor(100, USD)), 250_000_000)
    print(f"offer with 250 ms timeout -> {accepted}; clock advanced {clock.nanos() // 1_000_000} ms; "
          f"producer waits = {timed.stats().producer_waits}")
    first = timed.poll(0)
    second = timed.poll(0)
    print(f"poll(0) on the one-item queue -> {first.id if first else 'empty'}; poll(0) again -> {second.id if second else 'empty'}")

    print("\n== Backpressure: a fast producer, a slow consumer, capacity 4 ==")
    pipe: BoundedBlockingQueue[Payment] = BoundedBlockingQueue(4)
    script = Producer.script("auth", 20, 1_999)
    producer_done = threading.Event()
    Producer(pipe, script, producer_done).start()
    until_producer_waiting(pipe)                      # the consumer does not start until the producer is stuck
    print(f"producer blocked with {pipe.size()}/{pipe.capacity()} items queued; it cannot run ahead of the ledger")
    posted = Money.of_minor(0, USD)
    fifo = True
    for expected in script:
        p = pipe.take()
        fifo &= p.id == expected.id
        posted = posted.plus(p.amount)
    producer_done.wait()
    stats = pipe.stats()
    print(f"consumed {len(script)} payments in FIFO order: {fifo}; posted {posted}")
    print(f"high-water mark {stats.high_water_mark} (never above capacity), producer waited {stats.producer_waits} times, "
          f"consumer waited {stats.consumer_waits} times")

    print("\n== Without the bound, the backlog lives in your heap ==")
    wide: BoundedBlockingQueue[Payment] = BoundedBlockingQueue(1_000)
    for p in script:
        wide.put(p)
    print(f"capacity 1000: all 20 queued, producer waited {wide.stats().producer_waits} times, "
          f"high-water mark {wide.stats().high_water_mark}: nothing told the producer to slow down")


if __name__ == "__main__":
    main()
