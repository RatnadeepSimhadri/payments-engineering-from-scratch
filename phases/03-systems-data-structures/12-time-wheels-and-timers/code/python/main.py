"""Lesson: phases/03-systems-data-structures/12-time-wheels-and-timers/docs/en.md
Hierarchical Timing Wheels
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Varghese & Lauck, "Hashed and Hierarchical Timing Wheels" (SOSP 1987); Apache Kafka TimingWheel /
SystemTimer (purgatory); Netty HashedWheelTimer. Python 3.11+, standard library only.
"""

from __future__ import annotations

import heapq
from typing import Callable, Optional, Protocol

LONG_MIN, LONG_MAX = -(1 << 63), (1 << 63) - 1


def add_exact(a: int, b: int) -> int:
    """Math.addExact: Python ints never overflow, so a long overflow is checked explicitly."""
    r = a + b
    if not LONG_MIN <= r <= LONG_MAX:
        raise OverflowError("long overflow")
    return r


def multiply_exact(a: int, b: int) -> int:
    r = a * b
    if not LONG_MIN <= r <= LONG_MAX:
        raise OverflowError("long overflow")
    return r


def ceil_div(a: int, b: int) -> int:
    """Math.ceilDiv for a positive divisor."""
    return -(-a // b)


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


class Clock(Protocol):
    """Injected time source. Tests and the demo use ManualClock; production would read time.monotonic_ns()."""

    def now_millis(self) -> int: ...


class ManualClock:
    def __init__(self, start_millis: int) -> None:
        self._now = start_millis

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError("time does not go backwards")
        self._now += millis


class TimerEntry:
    """A scheduled callback, doubly linked so cancellation unlinks in O(1) without searching the bucket."""

    __slots__ = ("id", "deadline_millis", "task", "prev", "next", "bucket", "_cancelled", "_fired")

    def __init__(self, id: int, deadline_millis: int, task: Callable[[], None]) -> None:
        if task is None:
            raise TypeError("task")
        self.id = id
        self.deadline_millis = deadline_millis
        self.task = task
        self.prev: Optional[TimerEntry] = None
        self.next: Optional[TimerEntry] = None
        self.bucket: Optional[Bucket] = None
        self._cancelled = False
        self._fired = False

    def cancel(self) -> bool:
        """O(1): unlink from whatever bucket holds this entry. False if already cancelled or fired."""
        if self._cancelled or self._fired:
            return False
        self._cancelled = True
        if self.bucket is not None:
            self.bucket.remove(self)
        return True

    def fire(self) -> bool:
        """Run the task once. False if the entry was cancelled or has already fired."""
        if self._cancelled or self._fired:
            return False
        self._fired = True
        self.task()
        return True

    def is_cancelled(self) -> bool:
        return self._cancelled

    def has_fired(self) -> bool:
        return self._fired


class Bucket:
    """One slot of a wheel: a circular doubly linked list with a sentinel. add/remove O(1); flush O(k)."""

    def __init__(self) -> None:
        self._sentinel = TimerEntry(-1, LONG_MIN, lambda: None)
        self._sentinel.prev = self._sentinel
        self._sentinel.next = self._sentinel
        self._size = 0

    def add(self, e: TimerEntry) -> None:
        s = self._sentinel
        e.bucket = self
        e.prev = s.prev
        e.next = s
        s.prev.next = e
        s.prev = e
        self._size += 1

    def remove(self, e: TimerEntry) -> None:
        if e.bucket is not self:
            return
        e.prev.next = e.next
        e.next.prev = e.prev
        e.prev = e.next = None
        e.bucket = None
        self._size -= 1

    def flush(self, consumer: Callable[[TimerEntry], None]) -> None:
        """Remove every entry and hand each to the consumer, in insertion order."""
        if self._size == 0:
            return
        e = self._sentinel.next
        while e is not self._sentinel:
            nxt = e.next
            self.remove(e)
            consumer(e)
            e = nxt

    def size(self) -> int:
        return self._size


class TimingWheel:
    """One level: wheel_size buckets of tick_millis each. Deadlines beyond the interval go to an overflow wheel."""

    def __init__(self, tick_millis: int, wheel_size: int, start_millis: int) -> None:
        if tick_millis <= 0 or wheel_size <= 1:
            raise ValueError("tick must be > 0 and wheelSize > 1")
        self.tick_millis = tick_millis
        self.wheel_size = wheel_size
        self.interval = multiply_exact(tick_millis, wheel_size)
        self._current_time = start_millis - start_millis % tick_millis  # Python % is Math.floorMod here
        self._buckets = [Bucket() for _ in range(wheel_size)]
        self._overflow: Optional[TimingWheel] = None
        self.cascades = 0  # entries moved down from this wheel's overflow

    def current_time(self) -> int:
        return self._current_time

    def levels(self) -> int:
        return 1 if self._overflow is None else 1 + self._overflow.levels()

    def add(self, e: TimerEntry) -> bool:
        """Place an entry. False if its deadline falls inside the current tick (already due): the caller runs it."""
        deadline = e.deadline_millis
        if e.is_cancelled():
            return True  # nothing to do, nothing to run
        if deadline < self._current_time + self.tick_millis:
            return False  # due now
        if deadline < self._current_time + self.interval:
            virtual_id = deadline // self.tick_millis  # Math.floorDiv
            self._buckets[virtual_id % self.wheel_size].add(e)
            return True
        if self._overflow is None:
            self._overflow = TimingWheel(self.interval, self.wheel_size, self._current_time)
        return self._overflow.add(e)

    def advance_to(self, time_millis: int, due: Callable[[TimerEntry], None]) -> None:
        """Advance tick by tick; each tick cascades the overflow wheels, then expires this wheel's bucket."""

        def expire(e: TimerEntry) -> None:
            if not e.is_cancelled():
                due(e)

        while self._current_time + self.tick_millis <= time_millis:
            self._current_time += self.tick_millis
            if self._overflow is not None:
                self._overflow._cascade_to(self._current_time, self, due)
            self._buckets[(self._current_time // self.tick_millis) % self.wheel_size].flush(expire)

    def _cascade_to(self, time_millis: int, root: "TimingWheel", due: Callable[[TimerEntry], None]) -> None:
        """Called from below when the lowest wheel reaches a time that may be one of our tick boundaries."""
        if time_millis < self._current_time + self.tick_millis:
            return  # not our boundary yet
        self._current_time = time_millis - time_millis % self.tick_millis
        if self._overflow is not None:
            self._overflow._cascade_to(self._current_time, root, due)

        def reinsert(e: TimerEntry) -> None:
            if e.is_cancelled():
                return
            root.cascades += 1
            if not root.add(e):
                due(e)  # deadline inside the current lowest tick

        self._buckets[(self._current_time // self.tick_millis) % self.wheel_size].flush(reinsert)

    def pending(self) -> int:
        n = sum(b.size() for b in self._buckets)
        return n + (0 if self._overflow is None else self._overflow.pending())


class HierarchicalTimer:
    """The public face: schedule by delay, cancel by handle, and drive from an injected clock."""

    def __init__(self, clock: Clock, tick_millis: int, wheel_size: int) -> None:
        self._clock = clock
        self._wheel = TimingWheel(tick_millis, wheel_size, clock.now_millis())
        self._next_id = 1
        self._fired = 0

    def schedule(self, delay_millis: int, task: Callable[[], None]) -> TimerEntry:
        """Deadline rounded UP to the next tick multiple: never early, at most one tick late."""
        if delay_millis < 0:
            raise ValueError("delay must be >= 0")
        deadline = add_exact(self._clock.now_millis(), delay_millis)
        tick = self._wheel.tick_millis
        rounded = multiply_exact(ceil_div(deadline, tick), tick)
        e = TimerEntry(self._next_id, rounded, task)
        self._next_id += 1
        if not self._wheel.add(e) and e.fire():  # already due: run inline
            self._fired += 1
        return e

    def poll(self) -> None:
        """Process everything due at or before the clock's current time."""

        def run(e: TimerEntry) -> None:
            if e.fire():
                self._fired += 1

        self._wheel.advance_to(self._clock.now_millis(), run)

    def pending(self) -> int:
        return self._wheel.pending()

    def fired(self) -> int:
        return self._fired

    def levels(self) -> int:
        return self._wheel.levels()

    def cascades(self) -> int:
        return self._wheel.cascades

    def tick_millis(self) -> int:
        return self._wheel.tick_millis


class HeapTimer:
    """The usual comparison: a binary heap keyed by (deadline, id), with lazy cancellation."""

    def __init__(self, clock: Clock) -> None:
        self._clock = clock
        self._heap: list[tuple[int, int, TimerEntry]] = []
        self._next_id = 1
        self._fired = 0

    def schedule(self, delay_millis: int, task: Callable[[], None]) -> TimerEntry:
        e = TimerEntry(self._next_id, add_exact(self._clock.now_millis(), delay_millis), task)
        self._next_id += 1
        heapq.heappush(self._heap, (e.deadline_millis, e.id, e))  # O(log n)
        return e

    def poll(self) -> None:
        """Cancelled entries stay in the heap until they surface: O(1) cancel, memory until the deadline."""
        now = self._clock.now_millis()
        while self._heap and self._heap[0][0] <= now:
            _, _, e = heapq.heappop(self._heap)
            if e.fire():
                self._fired += 1

    def pending(self) -> int:
        return len(self._heap)

    def fired(self) -> int:
        return self._fired


def same_set_fired(a: list[int], b: list[int]) -> bool:
    """Within a tick the two structures may order differently, so compare the fired sets, not the sequences."""
    return len(a) == len(b) and sorted(a) == sorted(b)


def java_list(xs: list) -> str:
    return "[" + ", ".join(str(x) for x in xs) + "]"


def java_bool(b: bool) -> str:
    return "true" if b else "false"


def main() -> None:
    print("== 1. Authorization holds: schedule, fire, cancel on capture ==")
    clock = ManualClock(1_000_000)
    timer = HierarchicalTimer(clock, 1, 8)  # 1 ms tick, 8 buckets per level
    log: list[str] = []
    timer.schedule(3, lambda: log.append("auth-A expired"))
    auth_b = timer.schedule(5, lambda: log.append("auth-B expired"))
    timer.schedule(5, lambda: log.append("auth-C expired"))
    timer.schedule(12, lambda: log.append("auth-D expired (beyond the 8 ms wheel: overflow level)"))
    print(f"  levels after scheduling = {timer.levels()}, pending = {timer.pending()}")
    clock.advance(4)
    timer.poll()
    print(f"  t+4:  {java_list(log)}")
    cancelled = auth_b.cancel()
    print(f"  auth-B captured, cancel() = {java_bool(cancelled)} (O(1) unlink), pending = {timer.pending()}")
    clock.advance(8)
    timer.poll()
    print(f"  t+12: {java_list(log)}")
    print(f"  cascades from the overflow wheel = {timer.cascades()}")

    print()
    print("== 2. Three levels: 1 ms x 8 covers 8 ms, 8 ms x 8 covers 64 ms, 64 ms x 8 covers 512 ms ==")
    clock2 = ManualClock(0)
    t2 = HierarchicalTimer(clock2, 1, 8)
    fire_times: list[int] = []
    delays = [1, 7, 8, 9, 63, 64, 65, 300, 511]
    for d in delays:
        t2.schedule(d, lambda: fire_times.append(clock2.now_millis()))
    print(f"  delays {java_list(delays)} -> levels = {t2.levels()}")
    last = 0
    for d in delays:
        clock2.advance(d - last)
        last = d
        t2.poll()
    print(f"  fired at   {java_list(fire_times)} (each at exactly its deadline)")
    print(f"  cascades = {t2.cascades()} (one per entry per level it descended)")

    print()
    print("== 3. Tick granularity: a 10 ms tick cannot tell 23 ms from 29 ms ==")
    clock3 = ManualClock(0)
    coarse = HierarchicalTimer(clock3, 10, 16)
    order: list[str] = []
    coarse.schedule(29, lambda: order.append("29ms"))
    coarse.schedule(23, lambda: order.append("23ms"))
    clock3.advance(25)
    coarse.poll()
    print(f"  at t=25 fired: {java_list(order)} (nothing: both sit in the tick that ends at 30)")
    clock3.advance(5)
    coarse.poll()
    print(f"  at t=30 fired: {java_list(order)} (insertion order, not deadline order, within a tick)")

    print()
    print("== 4. Wheel versus heap on the same seeded workload ==")
    rng = JavaRandom(2024)
    wc, hc = ManualClock(0), ManualClock(0)
    wheel_t = HierarchicalTimer(wc, 1, 64)
    heap_t = HeapTimer(hc)
    wheel_order: list[int] = []
    heap_order: list[int] = []
    n = 200_000
    wheel_entries: list[TimerEntry] = []
    heap_entries: list[TimerEntry] = []
    for i in range(n):
        delay = 1 + rng.next_int(600_000)  # up to 10 minutes
        wheel_entries.append(wheel_t.schedule(delay, lambda i=i: wheel_order.append(i)))
        heap_entries.append(heap_t.schedule(delay, lambda i=i: heap_order.append(i)))
    cancelled_count = 0
    for i in range(0, n, 3):  # capture one in three holds
        wheel_entries[i].cancel()
        heap_entries[i].cancel()
        cancelled_count += 1
    print(f"  scheduled {n}, cancelled {cancelled_count} -> wheel pending {wheel_t.pending()} (freed on cancel), "
          f"heap pending {heap_t.pending()} (freed lazily when polled)")
    print(f"  wheel levels = {wheel_t.levels()} for deadlines up to 600000 ms with 1 ms x 64 buckets")
    for _ in range(601):
        wc.advance(1_000)
        hc.advance(1_000)
        wheel_t.poll()
        heap_t.poll()
    print(f"  fired: wheel {wheel_t.fired()}, heap {heap_t.fired()}, expected {n - cancelled_count}")
    print(f"  same set of timers fired = {java_bool(same_set_fired(wheel_order, heap_order))}")
    print(f"  cascades = {wheel_t.cascades()} (each entry moves down at most levels-1 times)")

    print()
    print("== 5. Cost model ==")
    print("  wheel: insert O(1), cancel O(1), tick O(1 + due); an idle wheel still pays one tick per tickMillis")
    print("  heap:  insert O(log n), cancel O(1) lazy or O(n) eager, poll O(log n) per due; idle heap costs nothing")
    print(f"  with n = {n}: log2(n) = {n.bit_length()} comparisons per heap insert versus one bucket link")


if __name__ == "__main__":
    main()
