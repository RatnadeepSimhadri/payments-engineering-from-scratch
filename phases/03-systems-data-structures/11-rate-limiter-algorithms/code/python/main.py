"""Lesson: phases/03-systems-data-structures/11-rate-limiter-algorithms/docs/en.md
Rate Limiter Algorithms: Token Bucket, Leaky Bucket, Sliding Window
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: RFC 6585 (429 Too Many Requests); RFC 9110 section 10.2.3 (Retry-After); ATM Forum TM 4.0 / ITU-T I.371
(the Generic Cell Rate Algorithm, which the leaky bucket here is); Cloudflare (2017) on the sliding window counter.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass

SCALE = 1000  # tokens and water are tracked in thousandths so refill per millisecond is exact


# ----- clock ---------------------------------------------------------------------------------------


class Clock:
    def now_millis(self) -> int:
        raise NotImplementedError


class ManualClock(Clock):
    def __init__(self, start_millis: int = 0) -> None:
        self._now = start_millis

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError("clock cannot go backwards")
        self._now += millis

    def set(self, millis: int) -> None:
        if millis < self._now:
            raise ValueError("clock cannot go backwards")
        self._now = millis


# ----- the decision every limiter returns ----------------------------------------------------------


@dataclass(frozen=True)
class Decision:
    """allowed: serve the request. retry_after_millis: when denied, the earliest time a retry can
    succeed (becomes the Retry-After header, rounded up to whole seconds). delay_millis: when
    allowed by a shaper, how long the request waits before it is processed."""

    allowed: bool
    retry_after_millis: int = 0
    delay_millis: int = 0

    @staticmethod
    def allow(delay_millis: int = 0) -> "Decision":
        return Decision(True, 0, delay_millis)

    @staticmethod
    def deny(retry_after_millis: int) -> "Decision":
        return Decision(False, max(1, retry_after_millis), 0)

    def retry_after_header(self) -> int:
        """Retry-After is delta-seconds; always round up so the client never retries early."""
        return -(-self.retry_after_millis // 1000)


class RateLimiter:
    name = "base"

    def try_acquire(self) -> Decision:
        raise NotImplementedError


def _require_positive(name: str, value: int) -> None:
    if value <= 0:
        raise ValueError(f"{name} must be positive: {value}")


# ----- 1. token bucket -----------------------------------------------------------------------------


class TokenBucket(RateLimiter):
    """capacity tokens, refilled continuously at refill_per_second. A request takes one token.
    Bursts up to capacity are admitted instantly; the long-run rate is refill_per_second."""

    name = "token bucket"

    def __init__(self, capacity: int, refill_per_second: int, clock: Clock) -> None:
        _require_positive("capacity", capacity)
        _require_positive("refill_per_second", refill_per_second)
        self.capacity = capacity
        self.refill_per_second = refill_per_second
        self.clock = clock
        self._tokens = capacity * SCALE          # start full
        self._last_refill = clock.now_millis()

    def _refill(self) -> None:
        now = self.clock.now_millis()
        elapsed = now - self._last_refill
        if elapsed > 0:
            # refill_per_second tokens/s == refill_per_second thousandths per ms: exact integer math
            self._tokens = min(self.capacity * SCALE, self._tokens + elapsed * self.refill_per_second)
            self._last_refill = now

    def tokens(self) -> float:
        self._refill()
        return self._tokens / SCALE

    def try_acquire(self) -> Decision:
        self._refill()
        if self._tokens >= SCALE:
            self._tokens -= SCALE
            return Decision.allow()
        deficit = SCALE - self._tokens
        return Decision.deny(-(-deficit // self.refill_per_second))  # ceil(deficit / rate) ms


# ----- 2. leaky bucket (a shaper: constant outflow, bounded queue) --------------------------------


class LeakyBucket(RateLimiter):
    """Requests leave at exactly leak_per_second; up to queue_capacity may wait. Implemented with
    virtual scheduling (the GCRA): next_slot is the time the next request will be released."""

    name = "leaky bucket"

    def __init__(self, queue_capacity: int, leak_per_second: int, clock: Clock) -> None:
        _require_positive("queue_capacity", queue_capacity)
        _require_positive("leak_per_second", leak_per_second)
        if 1000 % leak_per_second != 0:
            raise ValueError("leak_per_second must divide 1000 so the interval is a whole millisecond")
        self.queue_capacity = queue_capacity
        self.interval_millis = 1000 // leak_per_second
        self.clock = clock
        self._next_slot = clock.now_millis()

    def queued(self) -> int:
        now = self.clock.now_millis()
        return max(0, (self._next_slot - now + self.interval_millis - 1) // self.interval_millis)

    def try_acquire(self) -> Decision:
        now = self.clock.now_millis()
        slot = max(now, self._next_slot)
        delay = slot - now
        if delay >= self.queue_capacity * self.interval_millis:
            # the queue is full: the request would wait longer than queue_capacity slots
            return Decision.deny(delay - self.queue_capacity * self.interval_millis + self.interval_millis)
        self._next_slot = slot + self.interval_millis
        return Decision.allow(delay)


# ----- 3. fixed window -----------------------------------------------------------------------------


class FixedWindow(RateLimiter):
    """limit requests per window aligned to multiples of window_millis. Cheap; admits up to 2x limit
    across a window boundary."""

    name = "fixed window"

    def __init__(self, limit: int, window_millis: int, clock: Clock) -> None:
        _require_positive("limit", limit)
        _require_positive("window_millis", window_millis)
        self.limit = limit
        self.window_millis = window_millis
        self.clock = clock
        self._window_start = -1
        self._count = 0

    def try_acquire(self) -> Decision:
        now = self.clock.now_millis()
        start = now - now % self.window_millis
        if start != self._window_start:
            self._window_start = start
            self._count = 0
        if self._count < self.limit:
            self._count += 1
            return Decision.allow()
        return Decision.deny(start + self.window_millis - now)


# ----- 4. sliding window log -----------------------------------------------------------------------


class SlidingWindowLog(RateLimiter):
    """Exact: keeps the timestamp of every admitted request in the last window_millis.
    O(limit) memory per key."""

    name = "sliding log"

    def __init__(self, limit: int, window_millis: int, clock: Clock) -> None:
        _require_positive("limit", limit)
        _require_positive("window_millis", window_millis)
        self.limit = limit
        self.window_millis = window_millis
        self.clock = clock
        self._log: deque[int] = deque()

    def try_acquire(self) -> Decision:
        now = self.clock.now_millis()
        cutoff = now - self.window_millis
        while self._log and self._log[0] <= cutoff:
            self._log.popleft()
        if len(self._log) < self.limit:
            self._log.append(now)
            return Decision.allow()
        return Decision.deny(self._log[0] + self.window_millis - now)

    def __len__(self) -> int:
        return len(self._log)


# ----- 5. sliding window counter -------------------------------------------------------------------


class SlidingWindowCounter(RateLimiter):
    """Approximate: two counters (previous and current fixed window), with the previous one weighted
    by how much of it still overlaps the sliding window. O(1) memory."""

    name = "sliding counter"

    def __init__(self, limit: int, window_millis: int, clock: Clock) -> None:
        _require_positive("limit", limit)
        _require_positive("window_millis", window_millis)
        self.limit = limit
        self.window_millis = window_millis
        self.clock = clock
        self._window_start = -1
        self._current = 0
        self._previous = 0

    def _roll(self, now: int) -> int:
        start = now - now % self.window_millis
        if start != self._window_start:
            if start == self._window_start + self.window_millis:
                self._previous = self._current       # the window just before this one
            else:
                self._previous = 0                   # a gap: nothing overlaps
            self._current = 0
            self._window_start = start
        return now - start

    def weighted_count_scaled(self) -> int:
        """(previous * remaining_overlap + current * window), i.e. the weighted count times window_millis.
        Kept scaled so the comparison is exact integer arithmetic."""
        elapsed = self._roll(self.clock.now_millis())
        return self._previous * (self.window_millis - elapsed) + self._current * self.window_millis

    def try_acquire(self) -> Decision:
        now = self.clock.now_millis()
        elapsed = self._roll(now)
        w = self.window_millis
        scaled = self._previous * (w - elapsed) + self._current * w
        if scaled + w <= self.limit * w:
            self._current += 1
            return Decision.allow()
        if self._current >= self.limit or self._previous == 0:
            return Decision.deny(w - elapsed)
        # smallest e' with previous*(w - e') + current*w + w <= limit*w
        needed = w - ((self.limit - 1 - self._current) * w) // self._previous
        return Decision.deny(max(1, needed - elapsed))


# ----- a trace: a retry storm against a 10 rps limit --------------------------------------------


def retry_storm_trace() -> list[int]:
    """Timestamps in ms. 3 s at a steady 10 rps (every 100 ms), then at t=3000 ms a burst of 40 retries
    within 20 ms, then steady 10 rps again until t=6000."""
    trace = [t for t in range(0, 3000, 100)]
    trace += [3000 + (i % 20) for i in range(40)]
    trace += [t for t in range(3100, 6000, 100)]
    return sorted(trace)


def make_limiters() -> list[tuple[RateLimiter, ManualClock]]:
    """Five limiters configured for the same nominal 10 requests per second, each with its own clock."""
    clocks = [ManualClock() for _ in range(5)]
    return [
        (TokenBucket(capacity=10, refill_per_second=10, clock=clocks[0]), clocks[0]),
        (LeakyBucket(queue_capacity=10, leak_per_second=10, clock=clocks[1]), clocks[1]),
        (FixedWindow(limit=10, window_millis=1000, clock=clocks[2]), clocks[2]),
        (SlidingWindowLog(limit=10, window_millis=1000, clock=clocks[3]), clocks[3]),
        (SlidingWindowCounter(limit=10, window_millis=1000, clock=clocks[4]), clocks[4]),
    ]


@dataclass
class ReplaySummary:
    name: str
    allowed: int
    denied: int
    allowed_in_burst: int
    max_in_any_second: int
    max_retry_after_millis: int
    max_delay_millis: int


def replay(limiter: RateLimiter, clock: ManualClock, trace: list[int], burst_start: int, burst_end: int) -> ReplaySummary:
    """Drive the injected clock through the trace and tally what the limiter did."""
    allowed_times: list[int] = []
    denied = 0
    in_burst = 0
    max_retry = 0
    max_delay = 0
    for t in trace:
        clock.set(t)
        d = limiter.try_acquire()
        if d.allowed:
            allowed_times.append(t + d.delay_millis)
            max_delay = max(max_delay, d.delay_millis)
            if burst_start <= t < burst_end:
                in_burst += 1
        else:
            denied += 1
            max_retry = max(max_retry, d.retry_after_millis)
    return ReplaySummary(limiter.name, len(allowed_times), denied, in_burst, max_in_window(allowed_times, 1000), max_retry, max_delay)


def max_in_window(times: list[int], window_millis: int) -> int:
    """Largest number of admitted requests whose (processing) times fall in any half-open window of width window_millis."""
    times = sorted(times)
    best = 0
    lo = 0
    for hi in range(len(times)):
        while times[hi] - times[lo] >= window_millis:
            lo += 1
        best = max(best, hi - lo + 1)
    return best


def main() -> None:
    print("== Token bucket: burst now, steady later ==")
    clock = ManualClock()
    tb = TokenBucket(capacity=5, refill_per_second=2, clock=clock)
    burst = [tb.try_acquire().allowed for _ in range(7)]
    print(f"7 requests at t=0 against capacity 5: {burst}")
    d = tb.try_acquire()
    print(f"denied: retry after {d.retry_after_millis} ms -> Retry-After: {d.retry_after_header()}")
    clock.advance(500)
    print(f"t=500ms: tokens = {tb.tokens():.1f}, request allowed = {tb.try_acquire().allowed}")

    print("\n== Leaky bucket: admits, but spaces the output ==")
    clock = ManualClock()
    lb = LeakyBucket(queue_capacity=3, leak_per_second=10, clock=clock)
    for i in range(5):
        d = lb.try_acquire()
        print(f"request {i} at t=0: {'queued, runs at +' + str(d.delay_millis) + ' ms' if d.allowed else 'rejected, Retry-After ' + str(d.retry_after_millis) + ' ms'}")

    print("\n== Fixed window: the 2x problem at the boundary ==")
    clock = ManualClock()
    fw = FixedWindow(limit=10, window_millis=1000, clock=clock)
    log = SlidingWindowLog(limit=10, window_millis=1000, clock=clock)
    admitted_fw = admitted_log = 0
    for t in list(range(950, 1000, 5)) + list(range(1000, 1050, 5)):
        clock.set(t)
        admitted_fw += fw.try_acquire().allowed
        admitted_log += log.try_acquire().allowed
    print(f"20 requests between t=950 and t=1050 ms, limit 10/s: fixed window admitted {admitted_fw}, sliding log admitted {admitted_log}")

    print("\n== Sliding counter: weighting the previous window ==")
    clock = ManualClock()
    sc = SlidingWindowCounter(limit=10, window_millis=1000, clock=clock)
    for t in range(0, 1000, 100):
        clock.set(t)
        sc.try_acquire()
    clock.set(1250)
    print(f"10 requests in window 0; at t=1250 weighted count = {sc.weighted_count_scaled() / 1000:.2f}, next request allowed = {sc.try_acquire().allowed}")
    clock.set(1500)
    print(f"at t=1500 weighted count = {sc.weighted_count_scaled() / 1000:.2f}, next request allowed = {sc.try_acquire().allowed}")

    print("\n== The same retry storm through all five (limit 10 rps, burst of 40 at t=3.0 s) ==")
    trace = retry_storm_trace()
    print(f"{len(trace)} requests over 6 s: 59 steady plus 40 in the burst; a limiter at exactly 10 rps admits 59 + 10")
    print(f"{'limiter':16} {'allowed':>7} {'denied':>6} {'in burst':>8} {'max/1s':>6} {'max Retry-After':>15} {'max delay':>9}")
    for limiter, clock in make_limiters():
        s = replay(limiter, clock, trace, 3000, 3020)
        print(f"{s.name:16} {s.allowed:>7} {s.denied:>6} {s.allowed_in_burst:>8} {s.max_in_any_second:>6} {s.max_retry_after_millis:>12} ms {s.max_delay_millis:>6} ms")


if __name__ == "__main__":
    main()
